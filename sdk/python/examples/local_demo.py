"""Everything in one process, no setup: an embedded relay, two nodes,
messaging, request/reply, a pipe and a file transfer.

    python local_demo.py
"""
import os
import tempfile
import threading

import magnetk


def main():
    with magnetk.Relay(listen="127.0.0.1:0") as relay:
        print("relay on", relay.address, "| QUIC:", magnetk.quic_available())
        alice = magnetk.Node(relays=[relay.address], id="alice")
        bob = magnetk.Node(relays=[relay.address], id="bob", listen=True, listen_addr="127.0.0.1:0")

        # 1. direct message with delivery receipt
        alice.send("bob", "hi bob", receipt=True)
        print("bob got:", bob.receive(timeout=5).text)

        # 2. publish / subscribe
        done = threading.Event()
        bob.subscribe("news.>", lambda m: (print("bob read", m.subject, m.text), done.set()))
        alice.publish("news.tech", "QUIC is fast")
        done.wait(5)

        # 3. request / reply
        bob.serve("upper", lambda m: m.text.upper())
        print("alice asked, bob answered:", alice.request_topic("upper", "make me loud").decode())

        # 4. a pipe (byte stream), peer-to-peer if possible
        def echo(p):
            with p:
                p.write(p.read_all())
                p.close_write()

        bob.handle_pipe("echo", echo)
        with alice.open_pipe("bob", "echo") as p:
            p.write(b"x" * 1_000_000)
            p.close_write()
            print("pipe echoed %d bytes (direct=%s)" % (len(p.read_all()), p.direct))

        # 5. file share + verified download
        with tempfile.TemporaryDirectory() as d:
            src = os.path.join(d, "demo.bin")
            with open(src, "wb") as f:
                f.write(os.urandom(8 * 1024 * 1024))
            shared = bob.share(src)
            res = alice.download(shared.link, os.path.join(d, "copy.bin"))
            print("downloaded %.1f MB in %.2fs" % (res.size / 1e6, res.seconds))
            bob.unshare(shared.hash)

        print("relay stats:", {k: relay.stats()[k] for k in ("nodes", "subscriptions", "files")})
        alice.close()
        bob.close()


if __name__ == "__main__":
    main()
