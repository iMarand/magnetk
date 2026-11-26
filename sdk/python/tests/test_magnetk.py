"""End-to-end tests of the Python SDK against an embedded relay."""

import os
import threading
import time

import pytest

import magnetk


@pytest.fixture(scope="module")
def relay():
    r = magnetk.Relay(listen="127.0.0.1:0")
    yield r
    r.close()


@pytest.fixture
def pair(relay):
    a = magnetk.Node(relays=[relay.address], id="py-a-%d" % time.monotonic_ns(), cache_dir="-")
    b = magnetk.Node(relays=[relay.address], id="py-b-%d" % time.monotonic_ns(), cache_dir="-",
                     listen=True, listen_addr="127.0.0.1:0")
    yield a, b
    a.close()
    b.close()


def test_version():
    assert magnetk.version() == "2.0.0"
    assert isinstance(magnetk.quic_available(), bool)


def test_direct_message_and_receipt(pair):
    a, b = pair
    a.send(b.id, "hello", receipt=True)
    m = b.receive(timeout=5)
    assert m is not None and m.kind == "direct" and m.sender == a.id and m.text == "hello"
    with pytest.raises(magnetk.NoRouteError):
        a.send("nobody-here", b"x", receipt=True)


def test_pubsub_handler_wildcards_and_order(pair):
    a, b = pair
    got = []
    done = threading.Event()

    def on_msg(m):
        got.append(m.text)
        if len(got) == 50:
            done.set()

    sub = b.subscribe("metrics.*.cpu", on_msg)
    for i in range(50):
        a.publish("metrics.host1.cpu", str(i))
    a.publish("metrics.host1.mem", "ignored")
    assert done.wait(5)
    assert got == [str(i) for i in range(50)]
    sub.unsubscribe()


def test_queue_group_splits_load(relay, pair):
    a, b = pair
    c = magnetk.Node(relays=[relay.address], cache_dir="-")
    try:
        counts = {"b": 0, "c": 0}
        lock = threading.Lock()

        def counter(name):
            def f(_m):
                with lock:
                    counts[name] += 1
            return f

        b.subscribe("jobs", counter("b"), queue="workers")
        c.subscribe("jobs", counter("c"), queue="workers")
        for _ in range(40):
            a.publish("jobs", b"job")
        deadline = time.time() + 5
        while counts["b"] + counts["c"] < 40 and time.time() < deadline:
            time.sleep(0.02)
        assert counts["b"] + counts["c"] == 40
        assert counts["b"] > 0 and counts["c"] > 0
    finally:
        c.close()


def test_request_reply(pair):
    a, b = pair
    b.serve("math.add", lambda m: str(sum(int(x) for x in m.text.split("+"))))
    assert a.request_topic("math.add", "2+3") == b"5"

    def handler(m):
        if m.text == "boom":
            raise ValueError("kaboom")
        return {"echo": m.text}

    b.on_request(handler)
    assert a.request(b.id, "hi", timeout=5) == b'{"echo": "hi"}'
    with pytest.raises(magnetk.RemoteError) as ei:
        a.request(b.id, "boom", timeout=5)
    assert "kaboom" in str(ei.value)
    with pytest.raises(magnetk.NoRouteError):
        a.request("ghost", "x", timeout=5)


def test_manual_request_via_receive(pair):
    a, b = pair
    result = {}

    def ask():
        result["resp"] = a.request(b.id, "manual", timeout=5)

    t = threading.Thread(target=ask)
    t.start()
    m = b.receive(timeout=5)
    assert m is not None and m.is_request and m.text == "manual"
    m.respond("answered")
    t.join(5)
    assert result["resp"] == b"answered"


def next_presence_of(node, who, timeout=5):
    """Skip presence events of other nodes (earlier tests share the relay)."""
    deadline = time.time() + timeout
    while time.time() < deadline:
        m = node.receive(timeout=max(0.01, deadline - time.time()))
        if m is not None and m.kind == "presence" and m.sender == who:
            return m
    raise AssertionError("no presence event for %s" % who)


def test_presence(relay):
    w = magnetk.Node(relays=[relay.address], id="watcher-%d" % time.monotonic_ns(), watch_presence=True)
    try:
        x = magnetk.Node(relays=[relay.address], id="pres-x", meta="sensor")
        m = next_presence_of(w, "pres-x")
        assert m.subject == "join" and m.text == "sensor"
        ids = {n["node_id"] for n in w.nodes()}
        assert "pres-x" in ids
        x.close()
        m = next_presence_of(w, "pres-x")
        assert m.subject == "leave"
    finally:
        w.close()


@pytest.mark.parametrize("route", ["relay", "direct"])
def test_pipes(pair, route):
    a, b = pair

    def echo(p):
        with p:
            for chunk in p:
                p.write(chunk)
            p.close_write()

    b.handle_pipe("echo", echo)
    payload = os.urandom(3 * 1024 * 1024 + 17)
    with a.open_pipe(b.id, "echo", route=route) as p:
        assert p.direct == (route == "direct")
        writer = threading.Thread(target=lambda: (p.write(payload), p.close_write()))
        writer.start()
        data = p.read_all(timeout=20)
        writer.join()
    assert data == payload


def test_accept_pipe_and_reject(pair):
    a, b = pair
    with pytest.raises(magnetk.RejectedError):
        a.open_pipe(b.id, "nobody-listens", route="relay")
    b.accept_pipe(timeout=0)  # enable generic accepting
    with a.open_pipe(b.id, "raw", meta="m1", route="relay") as p:
        q = b.accept_pipe(timeout=5)
        assert q is not None and q.service == "raw" and q.meta == "m1" and q.peer == a.id
        p.write(b"abc")
        p.close_write()
        assert q.read_all(timeout=5) == b"abc"
        q.close()


def test_share_download_resume_progress(tmp_path, pair):
    a, b = pair
    src = tmp_path / "data.bin"
    data = os.urandom(5 * 1024 * 1024 + 3)
    src.write_bytes(data)
    shared = b.share(str(src))
    link = magnetk.Link.parse(shared.link)
    assert link.file_hash == shared.hash and link.file_size == len(data) and link.manifest_root
    updates = []
    out = tmp_path / "out"
    out.mkdir()
    res = a.download(shared.link, str(out), progress=updates.append, timeout=60)
    with open(res.path, "rb") as fh:
        assert fh.read() == data
    assert res.hash == shared.hash
    assert updates and updates[-1].percent == pytest.approx(100.0)
    prov = a.find_providers(shared.hash)
    assert b.id in prov["providers"]
    with pytest.raises(magnetk.NoProvidersError):
        a.download("0" * 64, str(out), timeout=10)


def test_unauthorized():
    r = magnetk.Relay(listen="127.0.0.1:0", tokens=["secret"])
    try:
        with pytest.raises(magnetk.UnauthorizedError):
            magnetk.Node(relays=[r.address], token="wrong", connect_timeout=5)
        n = magnetk.Node(relays=[r.address], token="secret")
        assert n.id
        n.close()
    finally:
        r.close()


def test_links_and_topics():
    h = "a" * 64
    l = magnetk.Link(file_hash=h, file_name="x y.bin", file_size=10, relays=["1.2.3.4:4002", "relay2:4002"])
    p = magnetk.Link.parse(str(l))
    assert p.file_name == "x y.bin" and p.relays == ["1.2.3.4:4002", "relay2:4002"]
    assert magnetk.normalize_relay("/ip4/9.9.9.9/tcp/4001/p2p/X") == "9.9.9.9:4002"
    assert magnetk.match_topic("a.*.c", "a.b.c") and magnetk.match_topic("a.>", "a.b.c")
    assert not magnetk.match_topic("a.>", "a") and not magnetk.match_topic("a.*", "a.b.c")


def test_info_and_ping(pair):
    a, _ = pair
    info = a.info()
    assert info["connected"] and info["protocol"] in ("quic", "tcp")
    assert 0 < a.ping() < 5
    stats = a.relay_stats()
    assert stats["nodes"] >= 2


def test_secure_chat(relay, tmp_path):
    """End-to-end encrypted, signed chat with message requests and safety numbers."""
    suffix = time.monotonic_ns()
    alice = magnetk.Node(relays=[relay.address], id="py-alice-%d" % suffix, cache_dir="-",
                         identity_file=str(tmp_path / "alice.key"), chat_dir=str(tmp_path / "a"))
    bob = magnetk.Node(relays=[relay.address], id="py-bob-%d" % suffix, cache_dir="-",
                       identity_file=str(tmp_path / "bob.key"), chat_dir=str(tmp_path / "b"),
                       chat_require_approval=True)
    try:
        ident = alice.identity()
        assert ident["address"].startswith("magnetk://" + alice.id + "@") and "key=" in ident["address"]

        sent = alice.chat_send(bob.id, "secret plan")
        assert sent.encrypted
        m = bob.chat_receive(timeout=5)
        assert m and m.kind == "text" and m.text == "secret plan" and m.encrypted and m.trusted
        assert m.request  # bob requires approval: alice is a stranger
        assert alice.chat_receive(timeout=0.5) is None  # no ack yet
        bob.chat_allow(alice.id)
        ack = alice.chat_receive(timeout=5)
        assert ack and ack.kind == "ack" and ack.text == sent.id

        bob.chat_send(alice.id, "here is a voice note", attachment=b"RIFF" + os.urandom(2000), mime="audio/wav")
        v = alice.chat_receive(timeout=5)
        assert v and v.kind == "audio" and v.encrypted and v.attachment_ok and os.path.exists(v.path)

        assert alice.safety_number(bob.id) == bob.safety_number(alice.id) != ""
        alice.mark_verified(bob.id)
        assert alice.contact(bob.id)["verified"]
    finally:
        alice.close()
        bob.close()


def test_range_compression_adaptive(tmp_path, pair):
    a, b = pair
    lines = "".join("%08d,sensor-%d,temp=%d.%d,status=ok\n" % (i, i % 17, 20 + i % 9, i % 10) for i in range(120000))
    src = tmp_path / "data.csv"
    src.write_text(lines)
    data = src.read_bytes()
    shared = b.share(str(src))
    # whole file: compressible text travels compressed, window adapts
    res = a.download(shared.link, str(tmp_path / "full.csv"))
    assert (tmp_path / "full.csv").read_bytes() == data
    assert 0 < res.wire_bytes < len(data) * 0.5
    # without compression the wire carries everything
    res = a.download(shared.link, str(tmp_path / "plain.csv"), compression=False, resume=False)
    assert res.wire_bytes >= len(data)
    # byte range: exactly those bytes, verified
    res = a.download(shared.link, str(tmp_path / "part.csv"), offset=1_000_003, length=777_777)
    assert (tmp_path / "part.csv").read_bytes() == data[1_000_003:1_000_003 + 777_777]
    assert res.offset == 1_000_003


def test_chat_large_file(relay, tmp_path):
    suffix = time.monotonic_ns()
    alice = magnetk.Node(relays=[relay.address], id="py-fa-%d" % suffix, cache_dir="-",
                         identity_file=str(tmp_path / "a.key"), chat_dir=str(tmp_path / "a"))
    bob = magnetk.Node(relays=[relay.address], id="py-fb-%d" % suffix, cache_dir="-",
                       identity_file=str(tmp_path / "b.key"), chat_dir=str(tmp_path / "b"))
    try:
        big = tmp_path / "video.bin"
        big.write_bytes(os.urandom(2 * 1024 * 1024))
        sent = alice.chat_send(bob.id, "big file", file=str(big))
        assert sent.kind == "file" and sent.encrypted and sent.link
        final = None
        deadline = time.monotonic() + 30
        while final is None and time.monotonic() < deadline:
            m = bob.chat_receive(timeout=5)
            if m and m.kind == "file" and not m.pending:
                final = m
        assert final and final.attachment_ok and final.encrypted and final.text == "big file"
        with open(final.path, "rb") as f:
            assert f.read() == big.read_bytes()
    finally:
        alice.close()
        bob.close()
