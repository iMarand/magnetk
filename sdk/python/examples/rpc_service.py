"""Request/reply: run several servers, call them from a client.

    python rpc_service.py server [relay]      # run as many as you like
    python rpc_service.py client [relay]

Servers of the same topic share the load automatically.
"""
import os
import sys
import time

import magnetk


def server(relay):
    with magnetk.Node(relays=relay) as node:
        def add(msg):
            a, b = msg.json()
            return {"sum": a + b, "served_by": node.id}

        node.serve("math.add", add)
        print("serving math.add as", node.id, "(Ctrl+C to stop)")
        try:
            while True:
                time.sleep(1)
        except KeyboardInterrupt:
            pass


def client(relay):
    with magnetk.Node(relays=relay) as node:
        for i in range(10):
            t0 = time.perf_counter()
            reply = node.request_topic("math.add", "[%d, %d]" % (i, i * 10), timeout=5)
            print(reply.decode(), "in %.2f ms" % ((time.perf_counter() - t0) * 1000))


if __name__ == "__main__":
    if len(sys.argv) < 2 or sys.argv[1] not in ("server", "client"):
        sys.exit(__doc__)
    relay = sys.argv[2] if len(sys.argv) > 2 else os.environ.get("MAGNETK_RELAYS", "127.0.0.1:4002")
    (server if sys.argv[1] == "server" else client)(relay)
