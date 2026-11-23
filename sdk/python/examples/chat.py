"""Tiny chat: a shared room (pub/sub) plus private messages.

    python chat.py <your name> [relay host:port]

Type a line to talk to the room, "@name text" for a private message.
"""
import os
import sys

import magnetk


def main():
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    name = sys.argv[1]
    relay = sys.argv[2] if len(sys.argv) > 2 else os.environ.get("MAGNETK_RELAYS", "127.0.0.1:4002")

    node = magnetk.Node(relays=relay, id=name, watch_presence=True)
    node.subscribe("chat.room", lambda m: m.sender != name and print("\r[room] %s: %s\n> " % (m.sender, m.text), end=""))

    def inbox():
        for m in node.messages():
            if m.kind == "presence":
                print("\r* %s %s\n> " % (m.sender, "joined" if m.subject == "join" else "left"), end="")
            elif m.kind == "direct":
                print("\r[private] %s: %s\n> " % (m.sender, m.text), end="")

    import threading
    threading.Thread(target=inbox, daemon=True).start()
    print("Connected as %s. Online: %s" % (name, ", ".join(n["node_id"] for n in node.nodes())))
    try:
        while True:
            line = input("> ").strip()
            if not line:
                continue
            if line.startswith("@") and " " in line:
                to, text = line[1:].split(" ", 1)
                try:
                    node.send(to, text, receipt=True, store=True)
                except magnetk.NoRouteError:
                    print("%s is offline" % to)
            else:
                node.publish("chat.room", line)
    except (KeyboardInterrupt, EOFError):
        pass
    finally:
        node.close()


if __name__ == "__main__":
    main()
