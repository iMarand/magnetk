"""Download a magnetk link (resumes automatically if interrupted).

    python download.py "<magnetk link>" [output dir]
"""
import sys

import magnetk


def main():
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    link = sys.argv[1]
    out = sys.argv[2] if len(sys.argv) > 2 else "."
    relays = magnetk.Link.parse(link).relays  # the link names its relays

    def show(p):
        print("\r%5.1f%%  %.1f MB/s  eta %.0fs  [%d source(s)]   " % (
            p.percent, p.bytes_per_sec / 1e6, p.eta, p.sources), end="", flush=True)

    with magnetk.Node(relays=relays) as node:
        res = node.download(link, out, progress=show)
        print("\nsaved %s (%.1f MB in %.1fs, verified %s...)" % (res.path, res.size / 1e6, res.seconds, res.hash[:16]))


if __name__ == "__main__":
    main()
