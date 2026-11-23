"""Share a file and keep serving it.

    python share.py <file> [relay host:port]
"""
import os
import sys
import time

import magnetk


def main():
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    path = sys.argv[1]
    relay = sys.argv[2] if len(sys.argv) > 2 else os.environ.get("MAGNETK_RELAYS", "127.0.0.1:4002")

    # listen=True also accepts peer-to-peer downloads (faster when reachable);
    # everything still works through the relay when it is not.
    with magnetk.Node(relays=relay, listen=True) as node:
        shared = node.share(path)
        print("Sharing %s (%d bytes)" % (shared.name, shared.size))
        print("Magnetk Link:", shared.link)
        print("Ctrl+C to stop.")
        try:
            while True:
                time.sleep(30)
                info = node.info()
                print("served %.1f MB so far" % (info["served_bytes"] / 1e6))
        except KeyboardInterrupt:
            pass


if __name__ == "__main__":
    main()
