# Magnetk Python SDK

Messaging, pub/sub, request/reply, byte pipes and verified multi-source
file transfer for Python 3.8+. The SDK calls the Magnetk engine in
`libmagnetk` (a native library built from `go/capi`) through `ctypes`: no
subprocesses, no compiler needed at install time, and the same code paths
and performance as the Go SDK and the `magnetk` CLI.

## Install

```bash
# from the repository root, after building the native library:
scripts/build.sh            # or scripts\build.bat on Windows
pip install ./sdk/python    # wheel contains magnetk/lib/libmagnetk.* and msquic
```

The library is looked up in this order: `MAGNETK_LIB` (full path),
`magnetk/lib/` inside the package, `<repo>/build/lib`, then the system
search path. `magnetk.version()` and `magnetk.quic_available()` confirm it
loaded.

## Quick start

```python
import magnetk

# Embedded relay for local testing; in production use your VPS: "vps:4002"
relay = magnetk.Relay(listen="127.0.0.1:0")

alice = magnetk.Node(relay.address, id="alice")
bob = magnetk.Node(relay.address, id="bob")

bob.subscribe("chat.>", lambda m: print(f"{m.sender} on {m.subject}: {m.text}"))
alice.publish("chat.general", "hello everyone")

alice.send("bob", b"direct hello", receipt=True)   # waits for delivery
print(bob.receive(timeout=5).text)

bob.serve("svc.upper", lambda m: m.text.upper())   # request handler
print(alice.request_topic("svc.upper", "make me loud"))  # b'MAKE ME LOUD'

alice.close(); bob.close(); relay.close()
```

`Node` and `Relay` are context managers (`with magnetk.Node(...) as n:`).

## Node

```python
magnetk.Node(relays, *, id=None, token=None, workers=8, **options)
```

`relays` is `"host:port"`, `"a:4002,b:4002"` or a list — several relays
give failover. Options (all optional, see `sdk/c/include/magnetk.h`):

| Option | Default | Meaning |
|---|---|---|
| `transport` | `"auto"` | `"auto"` (QUIC, TCP fallback), `"quic"`, `"tcp"` |
| `route` | `"auto"` | data path for pipes/files: `"auto"`, `"direct"`, `"relay"` |
| `listen` | `False` | accept peer-to-peer connections (needed to be a fast seeder) |
| `listen_addr`, `advertise`, `upnp` | | direct listener address, extra public addresses, router port mapping |
| `meta` | `""` | string other nodes see in presence/lookup |
| `watch_presence` | `False` | receive join/leave events |
| `request_timeout` | `10` | seconds, default for requests and receipts |
| `inbox_size`, `sub_buffer`, `slow_consumer_wait_ms` | 8192, 1024, 250 | incoming buffers and burst tolerance |
| `chunk_size`, `streams_per_source`, `cache_dir`, `link_relays` | auto, 4, user cache, relays | file transfer tuning |
| `verify_peer`, `ca_file` | `False` | require trusted relay certificates |
| `congestion`, `profile`, `connect_timeout`, `log` | `"cubic"`, `"max_throughput"`, 15 s, `False` | QUIC tuning, engine logs to stderr |

`workers` is the number of threads running message handlers.

Properties and health: `node.id`, `node.info()` (relay, protocol, observed
address, RTT), `node.ping()` (seconds), `node.nodes(prefix="")`,
`node.relay_stats()`, `node.wait_connected(timeout)`.

## Messages

```python
node.send(to, data, *, receipt=False, store=False, unreliable=False, timeout=10.0)
node.publish(topic, data, *, unreliable=False)
node.request(to, data=b"", timeout=10.0) -> bytes
node.request_topic(topic, data=b"", timeout=10.0) -> bytes
```

* `data` may be `bytes`, `bytearray`, `memoryview` or `str` (UTF-8).
* `receipt=True` waits until the relay delivered it (raises
  `NoRouteError` if the node is offline); `store=True` keeps it at the
  relay for an offline node; `unreliable=True` uses a QUIC datagram
  (lowest latency, may be lost, ~1 KB max).
* Topics are dot-separated tokens. Patterns: `a.b` exact, `a.*` one token,
  `a.>` one or more tokens.

Receiving:

```python
sub = node.subscribe("sensors.*.temp", handler)        # handler(msg) on a worker thread
sub = node.subscribe("jobs.>", handler, queue="workers")  # load-balanced group
sub.unsubscribe()

node.serve("svc.resize", lambda m: do(m.data))   # return bytes/str/None, or raise
node.on_request(lambda m: ...)                   # requests sent to this node id

msg = node.receive(timeout=1.0)                  # direct messages + handler-less subscriptions
for msg in node.messages(): ...
```

`Message`: `kind` (`"direct"`, `"publish"`, `"request"`, `"presence"`),
`sender`, `subject`, `data`, `text`, `json()`, `datagram`,
`is_request`, `respond(data)`, `respond_error(text)`.

Handlers for one subscription run in order. If a handler falls behind,
delivery waits up to `slow_consumer_wait_ms`, then drops (counted).

## Pipes (byte streams)

```python
def upper(pipe):
    data = pipe.read_all()
    pipe.write(data.upper())
    pipe.close()

bob.handle_pipe("upper", upper)                 # one thread per incoming pipe
p = alice.open_pipe("bob", "upper", route="auto")  # "auto" | "direct" | "relay"
p.write(b"hello"); p.close_write()
print(p.read_all(), p.direct)
```

`Pipe`: `read(n, timeout)`, `read_exactly(n)`, `read_all()`, `write(data)`,
`close_write()`, `close()`, attributes `peer`, `service`, `meta`,
`direct`. `node.accept_pipe(timeout)` accepts pipes for services without a
handler.

## Files

```python
shared = node.share("big.iso")          # hashes once (cached), serves it
print(shared.link)                       # magnetk:?xt=urn:sha256:...&relay=...

res = other.download(shared.link, "downloads/",
                     progress=lambda p: print(f"{p.percent:.1f}% {p.bytes_per_sec/1e6:.1f} MB/s"))
print(res.path, res.size, res.seconds)

dl = other.download_async(link, seed=True)   # keep seeding afterwards
while not dl.poll(0.5):
    print(dl.progress)
node.unshare(shared.hash)
node.find_providers(shared.hash)
```

Downloads use every provider in parallel (several streams each, pipelined
chunk requests), verify every chunk and the whole file, write to
`<name>.mkpart` and resume after interruption. Use `listen=True` on
seeders so downloaders can connect directly.

## Relay

```python
relay = magnetk.Relay(listen="0.0.0.0:4002", tokens=["secret"],
                      peers=["other-vps:4002"], cluster_secret="s3cret")
relay.address, relay.port, relay.stats(); relay.close()
```

For production run `magnetk relay` as a service instead
([docs/RELAY_DEPLOYMENT.md](../../docs/RELAY_DEPLOYMENT.md)).

## Errors

All derive from `magnetk.MagnetkError`: `TimeoutError` (also a builtin
`TimeoutError`), `NotConnectedError` (also `ConnectionError`),
`NoRouteError`, `UnauthorizedError`, `RemoteError` (handler raised),
`RejectedError` (pipe refused), `NoProvidersError`, `DroppedError`,
`InvalidError`, `ClosedError`.

## Links

```python
link = magnetk.Link.parse(uri)   # file_hash, file_name, file_size, chunk_size, manifest_root, relays
magnetk.normalize_relay("/ip4/1.2.3.4/tcp/4003")   # -> "1.2.3.4:4002"
```

## Examples and tests

`examples/`: `local_demo.py` (everything in one process), `chat.py`,
`rpc_service.py`, `share.py`, `download.py`.

```bash
python -m pytest sdk/python/tests      # 14 integration tests
```

## Threading and resources

All methods are thread-safe. Blocking calls release the GIL while waiting
in native code. Each `Node` uses one dispatcher thread plus `workers`
handler threads; the engine itself runs on Go's scheduler (`MAX_PROCS` /
`MAGNETK_MAX_PROCS` limits CPU cores, `MAGNETK_MEMORY_LIMIT_MB` the heap).
Always `close()` nodes (or use `with`) so the relay sees a clean leave.
