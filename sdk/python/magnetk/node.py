"""The Magnetk node: messaging, pipes and files."""

from __future__ import annotations

import ctypes
import json
import logging
import queue
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from ctypes import POINTER, byref, c_int, c_int64, c_uint64, c_void_p, create_string_buffer
from dataclasses import dataclass, field
from typing import Any, Callable, Dict, Iterator, List, Optional, Union

from . import _native as nat
from .errors import InvalidError, MagnetkError, TimeoutError
from .topics import match_topic

log = logging.getLogger("magnetk")

Data = Union[bytes, bytearray, memoryview, str]

KIND_NAMES = {1: "direct", 2: "publish", 3: "request", 4: "presence"}


def _ms(timeout: Optional[float]) -> int:
    """seconds (None = forever) -> milliseconds for the C API."""
    if timeout is None:
        return -1
    return max(0, int(timeout * 1000))


# ====================================================================== #
# Messages                                                               #
# ====================================================================== #

@dataclass
class Message:
    """An incoming message.

    kind     "direct" | "publish" | "request" | "presence"
    sender   node id of the sender (for presence: the node that joined/left)
    subject  topic for publications and topic requests; "join"/"leave" for presence
    data     payload bytes
    """

    kind: str
    sender: str
    subject: str
    data: bytes
    id: int = 0
    datagram: bool = False
    _request: int = field(default=0, repr=False)
    _node: Optional["Node"] = field(default=None, repr=False)
    _answered: bool = field(default=False, repr=False)

    @property
    def text(self) -> str:
        return self.data.decode("utf-8", "replace")

    def json(self) -> Any:
        return json.loads(self.data)

    @property
    def is_request(self) -> bool:
        return self.kind == "request"

    def respond(self, data: Data) -> None:
        """Answer a request."""
        self._answer(data, False)

    def respond_error(self, message: str) -> None:
        """Answer a request with an error (raised as RemoteError at the requester)."""
        self._answer(message, True)

    def _answer(self, data: Data, is_error: bool) -> None:
        if not self.is_request or self._node is None:
            raise InvalidError("message is not a request")
        if self._answered:
            raise InvalidError("request already answered")
        self._answered = True
        payload = nat.data_arg(data)
        err = nat.ErrBox()
        err.check(nat.lib.mk_respond(self._node._h, self._request, payload, len(payload), 1 if is_error else 0, err.ref))


class Subscription:
    """Returned by Node.subscribe; call unsubscribe() to stop."""

    def __init__(self, node: "Node", pattern: str, queue_group: Optional[str], handler: Optional[Callable]):
        self.node = node
        self.pattern = pattern
        self.queue = queue_group
        self.handler = handler
        self.active = True

    def unsubscribe(self) -> None:
        if self.active:
            self.active = False
            self.node._remove_subscription(self)

    def __enter__(self) -> "Subscription":
        return self

    def __exit__(self, *exc) -> None:
        self.unsubscribe()


# ====================================================================== #
# Pipes                                                                  #
# ====================================================================== #

class Pipe:
    """A reliable byte stream to another node (socket-like / file-like)."""

    def __init__(self, handle: int):
        self._h = handle
        info = nat.take_json(nat.lib.mk_pipe_info(handle)) or {}
        self.peer: str = info.get("peer", "")
        self.service: str = info.get("service", "")
        self.meta: str = info.get("meta", "")
        self.direct: bool = bool(info.get("direct", False))
        self._closed = False

    def write(self, data: Data, timeout: Optional[float] = None) -> int:
        """Write all of data (blocks while the peer is slow)."""
        payload = nat.data_arg(data)
        view = memoryview(payload)
        total = 0
        while total < len(view):
            chunk = bytes(view[total: total + (1 << 20)])
            err = nat.ErrBox()
            n = nat.lib.mk_pipe_write(self._h, chunk, len(chunk), _ms(timeout), err.ref)
            if n < 0:
                err.check(n)
            total += n
        return total

    sendall = write

    def read(self, n: int = 65536, timeout: Optional[float] = None) -> bytes:
        """Read up to n bytes; b"" means the peer finished (EOF)."""
        if n <= 0:
            return b""
        buf = create_string_buffer(n)
        err = nat.ErrBox()
        got = nat.lib.mk_pipe_read(self._h, buf, n, _ms(timeout), err.ref)
        if got < 0:
            err.check(got)
        return buf.raw[:got]

    recv = read

    def read_exactly(self, n: int, timeout: Optional[float] = None) -> bytes:
        out = bytearray()
        while len(out) < n:
            chunk = self.read(n - len(out), timeout)
            if not chunk:
                raise EOFError("pipe closed after %d of %d bytes" % (len(out), n))
            out += chunk
        return bytes(out)

    def read_all(self, timeout: Optional[float] = None) -> bytes:
        """Read until the peer closes its side."""
        out = bytearray()
        while True:
            chunk = self.read(1 << 20, timeout)
            if not chunk:
                return bytes(out)
            out += chunk

    def __iter__(self) -> Iterator[bytes]:
        while True:
            chunk = self.read()
            if not chunk:
                return
            yield chunk

    def close_write(self) -> None:
        """Tell the peer we are done sending (it reads EOF); reading continues."""
        if not self._closed:
            nat.lib.mk_pipe_close_write(self._h)

    def close(self) -> None:
        if not self._closed:
            self._closed = True
            nat.lib.mk_pipe_close(self._h)

    def __enter__(self) -> "Pipe":
        return self

    def __exit__(self, *exc) -> None:
        self.close()

    def __del__(self) -> None:
        try:
            self.close()
        except Exception:
            pass

    def __repr__(self) -> str:
        return "Pipe(peer=%r, service=%r, direct=%r)" % (self.peer, self.service, self.direct)


# ====================================================================== #
# Files                                                                  #
# ====================================================================== #

@dataclass
class SharedFile:
    hash: str
    name: str
    size: int
    chunk_size: int
    root: str
    path: str
    link: str


@dataclass
class Progress:
    name: str
    size: int
    done: int
    percent: float
    bytes_per_sec: float
    eta: float
    sources: int
    streams: int


@dataclass
class DownloadResult:
    path: str
    hash: str
    size: int
    bytes: int
    resumed: int
    seconds: float


class Download:
    """A running download (see Node.download_async)."""

    def __init__(self, handle: int):
        self._h = handle
        self._result: Optional[DownloadResult] = None
        self._error: Optional[MagnetkError] = None
        self.progress: Optional[Progress] = None

    def poll(self, timeout: float = 0.25) -> bool:
        """Wait up to timeout; True when finished (raises if it failed)."""
        if self._result is not None:
            return True
        if self._error is not None:
            raise self._error
        err = nat.ErrBox()
        pj = c_void_p()
        rc = nat.lib.mk_download_poll(self._h, _ms(timeout), byref(pj), err.ref)
        info = nat.take_json(pj.value) or {}
        self.progress = Progress(
            name=info.get("name", ""), size=info.get("size", 0), done=info.get("done", 0),
            percent=info.get("percent", 0.0), bytes_per_sec=info.get("bytes_per_sec", 0.0),
            eta=info.get("eta_ms", 0) / 1000.0, sources=info.get("sources", 0), streams=info.get("streams", 0))
        if rc == 0:
            return False
        if rc < 0:
            try:
                err.check(rc)
            except MagnetkError as e:
                self._error = e
                raise
        self._result = DownloadResult(
            path=info.get("path", ""), hash=info.get("hash", ""), size=info.get("size", 0),
            bytes=info.get("bytes", 0), resumed=info.get("resumed", 0), seconds=info.get("elapsed_ms", 0) / 1000.0)
        return True

    def wait(self, timeout: Optional[float] = None,
             progress: Optional[Callable[[Progress], None]] = None) -> DownloadResult:
        deadline = None if timeout is None else time.monotonic() + timeout
        while not self.poll(0.25):
            if progress and self.progress:
                progress(self.progress)
            if deadline is not None and time.monotonic() > deadline:
                raise TimeoutError("download still running")
        if progress and self.progress:
            progress(self.progress)
        assert self._result is not None
        return self._result

    def cancel(self) -> None:
        nat.lib.mk_download_cancel(self._h)

    def close(self) -> None:
        if self._h:
            nat.lib.mk_download_free(self._h)
            self._h = 0

    def __del__(self) -> None:
        try:
            self.close()
        except Exception:
            pass


# ====================================================================== #
# Node                                                                   #
# ====================================================================== #

class Node:
    """A Magnetk node.

    >>> node = Node(relays=["relay.example.com:4002"], id="alice")
    >>> node.send("bob", b"hello")
    >>> node.subscribe("chat.*", lambda m: print(m.sender, m.text))
    >>> node.close()

    All options map to the C API config (see magnetk.h): id, token, meta,
    transport ("auto"|"quic"|"tcp"), route ("auto"|"direct"|"relay"),
    listen (accept peer-to-peer connections), listen_addr, advertise,
    upnp, verify_peer, ca_file, watch_presence, request_timeout,
    inbox_size, sub_buffer, slow_consumer_wait_ms,
    chunk_size, streams_per_source, cache_dir, link_relays, congestion,
    profile, connect_timeout, log.
    """

    def __init__(self, relays: Union[str, List[str], None] = None, *, id: Optional[str] = None,  # noqa: A002
                 token: Optional[str] = None, workers: int = 8, **options: Any):
        if isinstance(relays, str):
            relays = [r.strip() for r in relays.split(",") if r.strip()]
        cfg: Dict[str, Any] = {"relays": relays or []}
        if id:
            cfg["id"] = id
        if token:
            cfg["token"] = token
        for key, value in options.items():
            if key == "request_timeout":
                cfg["request_timeout_ms"] = int(value * 1000)
            elif key == "connect_timeout":
                cfg["connect_timeout_ms"] = int(value * 1000)
            elif key == "heartbeat":
                cfg["heartbeat_ms"] = int(value * 1000)
            else:
                cfg[key] = value
        h = c_uint64()
        err = nat.ErrBox()
        err.check(nat.lib.mk_node_open(json.dumps(cfg).encode(), byref(h), err.ref))
        self._h = h.value
        self._closed = False
        self._lock = threading.RLock()
        self._subs: List[Subscription] = []
        self._services: Dict[str, Callable[[Message], Any]] = {}
        self._request_handler: Optional[Callable[[Message], Any]] = None
        self._pipe_handlers: Dict[str, Callable[[Pipe], None]] = {}
        self._inbox: "queue.Queue[Message]" = queue.Queue()
        self._pipe_inbox: "queue.Queue[Pipe]" = queue.Queue()
        self._pool = ThreadPoolExecutor(max_workers=workers, thread_name_prefix="magnetk-handler")
        self._stop = threading.Event()
        self._dispatcher = threading.Thread(target=self._dispatch_loop, name="magnetk-dispatch", daemon=True)
        self._dispatcher.start()
        self._acceptor: Optional[threading.Thread] = None

    # ---------- identity / state ----------

    @property
    def id(self) -> str:
        return nat.take_string(nat.lib.mk_node_id(self._h)) or ""

    def info(self) -> Dict[str, Any]:
        """Connection details and counters (relay, protocol, rtt_us, sent, received, ...)."""
        return nat.take_json(nat.lib.mk_node_info(self._h)) or {}

    def wait_connected(self, timeout: Optional[float] = None) -> None:
        err = nat.ErrBox()
        err.check(nat.lib.mk_node_wait_connected(self._h, _ms(timeout), err.ref))

    def ping(self, timeout: Optional[float] = 5.0) -> float:
        """Round trip to the relay, in seconds."""
        us = c_int64()
        err = nat.ErrBox()
        err.check(nat.lib.mk_ping(self._h, _ms(timeout), byref(us), err.ref))
        return us.value / 1e6

    def nodes(self, prefix: str = "") -> List[Dict[str, Any]]:
        """Connected nodes (across federated relays)."""
        err = nat.ErrBox()
        res = nat.lib.mk_list_nodes(self._h, prefix.encode(), err.ref)
        if not res:
            err.check(-1)
        return nat.take_json(res) or []

    def relay_stats(self) -> Dict[str, Any]:
        err = nat.ErrBox()
        res = nat.lib.mk_relay_stats(self._h, err.ref)
        if not res:
            err.check(-1)
        return nat.take_json(res) or {}

    def watch_presence(self, on: bool = True) -> None:
        """Receive kind="presence" messages when nodes join or leave."""
        err = nat.ErrBox()
        err.check(nat.lib.mk_watch_presence(self._h, 1 if on else 0, err.ref))

    # ---------- sending ----------

    def send(self, to: str, data: Data, *, receipt: bool = False, store: bool = False,
             unreliable: bool = False, timeout: Optional[float] = 10.0) -> None:
        """Send a direct message.

        receipt     wait for the relay to confirm delivery (raises NoRouteError if offline)
        store       keep it at the relay if `to` is offline, deliver on reconnect
        unreliable  QUIC datagram: lowest latency, may be lost or reordered
        """
        payload = nat.data_arg(data)
        flags = (1 if receipt else 0) | (2 if store else 0) | (4 if unreliable else 0)
        err = nat.ErrBox()
        err.check(nat.lib.mk_send(self._h, to.encode(), payload, len(payload), flags, _ms(timeout), err.ref))

    def publish(self, topic: str, data: Data, *, unreliable: bool = False) -> None:
        """Publish to every subscriber of topic."""
        payload = nat.data_arg(data)
        err = nat.ErrBox()
        err.check(nat.lib.mk_publish(self._h, topic.encode(), payload, len(payload), 4 if unreliable else 0, err.ref))

    def request(self, to: str, data: Data = b"", timeout: Optional[float] = 10.0) -> bytes:
        """Send a request to node `to` and return its response."""
        return self._request(to.encode(), None, data, timeout)

    def request_topic(self, topic: str, data: Data = b"", timeout: Optional[float] = 10.0) -> bytes:
        """Send a request to one of the nodes serving `topic`."""
        return self._request(None, topic.encode(), data, timeout)

    def _request(self, to, topic, data, timeout) -> bytes:
        payload = nat.data_arg(data)
        out = c_void_p()
        out_len = c_int()
        err = nat.ErrBox()
        err.check(nat.lib.mk_request(self._h, to, topic, payload, len(payload), _ms(timeout), byref(out), byref(out_len), err.ref))
        return nat.take_bytes(out.value, out_len.value)

    # ---------- receiving ----------

    def subscribe(self, pattern: str, handler: Optional[Callable[[Message], None]] = None,
                  queue: Optional[str] = None) -> Subscription:
        """Subscribe to a topic pattern ("a.b", "a.*", "a.>").

        handler is called for each message, in order, on the dispatcher
        thread (keep it short or hand work off). Without a handler the
        messages go to receive()/messages(). Nodes subscribing with the same
        `queue` name split the messages between them.
        """
        sub = Subscription(self, pattern, queue, handler)
        err = nat.ErrBox()
        err.check(nat.lib.mk_subscribe(self._h, pattern.encode(), nat.b(queue), err.ref))
        with self._lock:
            self._subs.append(sub)
        return sub

    def _remove_subscription(self, sub: Subscription) -> None:
        with self._lock:
            if sub in self._subs:
                self._subs.remove(sub)
            still = any(s.pattern == sub.pattern for s in self._subs)
        if not still and not self._closed:
            err = nat.ErrBox()
            nat.lib.mk_unsubscribe(self._h, sub.pattern.encode(), err.ref)
            err.message()

    def serve(self, topic: str, handler: Callable[[Message], Any]) -> None:
        """Serve requests sent to `topic` (see request_topic). Several nodes
        serving the same topic share the load. The handler's return value
        (bytes / str / JSON-serialisable) is the response; exceptions are
        sent back as RemoteError."""
        with self._lock:
            self._services[topic] = handler
        err = nat.ErrBox()
        err.check(nat.lib.mk_serve(self._h, topic.encode(), err.ref))

    def on_request(self, handler: Optional[Callable[[Message], Any]]) -> None:
        """Handle requests addressed to this node (see request)."""
        with self._lock:
            self._request_handler = handler

    def receive(self, timeout: Optional[float] = None) -> Optional[Message]:
        """Next message without a handler (direct, unhandled publications,
        requests, presence). Returns None on timeout."""
        try:
            return self._inbox.get(timeout=timeout)
        except queue.Empty:
            return None

    def messages(self, timeout: Optional[float] = None) -> Iterator[Message]:
        """Iterate over incoming messages until the node closes (or timeout
        passes without a message)."""
        while not self._closed:
            m = self.receive(timeout if timeout is not None else 0.5)
            if m is not None:
                yield m
            elif timeout is not None:
                return

    def _dispatch_loop(self) -> None:
        ptr = POINTER(nat.MkMessage)()
        while not self._stop.is_set():
            err = nat.ErrBox()
            rc = nat.lib.mk_recv(self._h, 200, byref(ptr), err.ref)
            if rc == 0:
                continue
            if rc < 0:
                msg = err.message()
                if self._stop.is_set():
                    return
                log.debug("receive error %d: %s", rc, msg)
                time.sleep(0.05)
                continue
            raw = ptr.contents
            m = Message(
                kind=KIND_NAMES.get(raw.kind, "unknown"),
                sender=ctypes.string_at(raw.sender).decode("utf-8", "replace") if raw.sender else "",
                subject=ctypes.string_at(raw.subject).decode("utf-8", "replace") if raw.subject else "",
                data=ctypes.string_at(raw.data, raw.len) if raw.data and raw.len > 0 else b"",
                id=raw.id, datagram=bool(raw.datagram), _request=raw.request, _node=self)
            nat.lib.mk_message_free(ptr)
            try:
                self._route(m)
            except Exception:  # never let a handler kill the dispatcher
                log.exception("magnetk message handler failed")

    def _route(self, m: Message) -> None:
        if m.kind == "request":
            with self._lock:
                handler = self._services.get(m.subject) if m.subject else self._request_handler
            if handler is not None:
                self._pool.submit(self._answer, handler, m)
                return
        elif m.kind == "publish":
            with self._lock:
                subs = [s for s in self._subs if s.handler and match_topic(s.pattern, m.subject)]
            if subs:
                for s in subs:
                    s.handler(m)
                return
        self._inbox.put(m)

    @staticmethod
    def _answer(handler: Callable[[Message], Any], m: Message) -> None:
        try:
            out = handler(m)
        except Exception as e:  # noqa: BLE001 - forwarded to the requester
            if not m._answered:
                m.respond_error("%s: %s" % (type(e).__name__, e))
            return
        if m._answered:
            return
        if out is None:
            out = b""
        elif not isinstance(out, (bytes, bytearray, memoryview, str)):
            out = json.dumps(out)
        m.respond(out)

    # ---------- pipes ----------

    def open_pipe(self, to: str, service: str, *, meta: str = "", route: Optional[str] = None,
                  timeout: Optional[float] = 15.0) -> Pipe:
        """Open a byte stream to `to`. route: None (node default), "auto",
        "direct" (peer-to-peer only) or "relay"."""
        h = c_uint64()
        err = nat.ErrBox()
        err.check(nat.lib.mk_pipe_open(self._h, to.encode(), service.encode(), meta.encode(), nat.b(route),
                                       _ms(timeout), byref(h), err.ref))
        return Pipe(h.value)

    def handle_pipe(self, service: str, handler: Callable[[Pipe], None]) -> None:
        """Run handler(pipe) in a thread for each incoming pipe of `service`."""
        with self._lock:
            self._pipe_handlers[service] = handler
        self._ensure_acceptor()

    def accept_pipe(self, timeout: Optional[float] = None) -> Optional[Pipe]:
        """Next incoming pipe for a service without handler (None on timeout)."""
        self._ensure_acceptor()
        try:
            return self._pipe_inbox.get(timeout=timeout)
        except queue.Empty:
            return None

    def _ensure_acceptor(self) -> None:
        with self._lock:
            if self._acceptor is None:
                self._acceptor = threading.Thread(target=self._accept_loop, name="magnetk-pipes", daemon=True)
                self._acceptor.start()

    def _accept_loop(self) -> None:
        while not self._stop.is_set():
            h = c_uint64()
            err = nat.ErrBox()
            rc = nat.lib.mk_pipe_accept(self._h, 200, byref(h), err.ref)
            if rc == 0:
                continue
            if rc < 0:
                err.message()
                if self._stop.is_set():
                    return
                time.sleep(0.05)
                continue
            p = Pipe(h.value)
            with self._lock:
                handler = self._pipe_handlers.get(p.service)
            if handler is None:
                self._pipe_inbox.put(p)
            else:
                threading.Thread(target=self._run_pipe_handler, args=(handler, p), daemon=True).start()

    @staticmethod
    def _run_pipe_handler(handler: Callable[[Pipe], None], p: Pipe) -> None:
        try:
            handler(p)
        except Exception:
            log.exception("magnetk pipe handler failed")
            p.close()

    # ---------- files ----------

    def share(self, path: str, chunk_size: int = 0) -> SharedFile:
        """Hash (cached), serve and register a file; returns its magnetk link."""
        out = c_void_p()
        err = nat.ErrBox()
        err.check(nat.lib.mk_share(self._h, str(path).encode(), int(chunk_size), byref(out), err.ref))
        return SharedFile(**nat.take_json(out.value))

    def unshare(self, file_hash: str) -> None:
        err = nat.ErrBox()
        err.check(nat.lib.mk_unshare(self._h, file_hash.encode(), err.ref))

    def find_providers(self, file_hash: str) -> Dict[str, Any]:
        err = nat.ErrBox()
        res = nat.lib.mk_find_providers(self._h, file_hash.encode(), err.ref)
        if not res:
            err.check(-1)
        return nat.take_json(res) or {}

    def download_async(self, link: str, out: Optional[str] = None, *, route: Optional[str] = None,
                       seed: bool = False, streams_per_source: int = 0, resume: bool = True) -> Download:
        """Start a download; returns a Download to poll/wait/cancel."""
        opts = {"seed": seed, "no_resume": not resume}
        if route:
            opts["route"] = route
        if streams_per_source:
            opts["streams_per_source"] = streams_per_source
        h = c_uint64()
        err = nat.ErrBox()
        err.check(nat.lib.mk_download_start(self._h, link.encode(), nat.b(str(out) if out else None),
                                            json.dumps(opts).encode(), byref(h), err.ref))
        return Download(h.value)

    def download(self, link: str, out: Optional[str] = None, *,
                 progress: Optional[Callable[[Progress], None]] = None, timeout: Optional[float] = None,
                 **options: Any) -> DownloadResult:
        """Download a magnetk link (or a 64-char hash) and wait for it.
        Every chunk is verified; interrupted downloads resume."""
        dl = self.download_async(link, out, **options)
        try:
            return dl.wait(timeout=timeout, progress=progress)
        except BaseException:
            dl.cancel()
            raise
        finally:
            dl.close()

    # ---------- lifecycle ----------

    def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        self._stop.set()
        self._dispatcher.join(timeout=2)
        if self._acceptor is not None:
            self._acceptor.join(timeout=2)
        self._pool.shutdown(wait=False)
        nat.lib.mk_node_close(self._h)

    def __enter__(self) -> "Node":
        return self

    def __exit__(self, *exc) -> None:
        self.close()

    def __repr__(self) -> str:
        return "Node(id=%r)" % (self.id if not self._closed else "<closed>")
