#!/usr/bin/env bash
# Install Magnetk on Linux or macOS.
#
#   scripts/install.sh                 desktop/client: binary on PATH + magnetk:// links
#   sudo scripts/install.sh --relay    a relay server (systemd service, Linux)
#   sudo scripts/install.sh --seed DIR a seed that shares every file in DIR (systemd, Linux)
#
# Options: --prefix DIR (install dir), --no-links (skip the link handler),
#          --bin PATH (binary to install, default build/bin/magnetk).
#
# What gets installed:
#   <prefix>/magnetk (+ libmsquic next to it)   the CLI, relay and seed
#   magnetk on PATH (symlink)
#   magnetk:// and magnetk:? link handler        via "magnetk install" (skipped on
#                                                servers without a desktop)
#   --relay / --seed: /etc/magnetk/settings.conf and a systemd unit
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ROLE=client
SEED_DIR=""
LINKS=1
BIN="$ROOT/build/bin/magnetk"
PREFIX=""

while [ $# -gt 0 ]; do
  case "$1" in
    --relay) ROLE=relay ;;
    --seed) ROLE=seed; SEED_DIR="${2:?--seed needs a directory}"; shift ;;
    --client) ROLE=client ;;
    --no-links) LINKS=0 ;;
    --prefix) PREFIX="${2:?}"; shift ;;
    --bin) BIN="${2:?}"; shift ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
  shift
done

if [ ! -x "$BIN" ]; then
  echo "magnetk binary not found at $BIN - build it first: scripts/build.sh (or scripts/build.sh tcp)" >&2
  exit 1
fi

if [ "$(id -u)" = 0 ]; then
  PREFIX="${PREFIX:-/opt/magnetk}"
  BINDIR=/usr/local/bin
else
  PREFIX="${PREFIX:-$HOME/.local/lib/magnetk}"
  BINDIR="$HOME/.local/bin"
fi

echo "=== installing magnetk into $PREFIX ==="
mkdir -p "$PREFIX" "$BINDIR"
install -m 0755 "$BIN" "$PREFIX/magnetk"
for f in "$(dirname "$BIN")"/libmsquic.*; do
  [ -e "$f" ] && cp -a "$f" "$PREFIX/"
done
ln -sf "$PREFIX/magnetk" "$BINDIR/magnetk"
echo "magnetk -> $BINDIR/magnetk"
case ":$PATH:" in *":$BINDIR:"*) ;; *) echo "note: add $BINDIR to PATH (e.g. in ~/.profile)";; esac
"$PREFIX/magnetk" version

if [ "$LINKS" = 1 ]; then
  echo "=== magnetk:// link handler ==="
  "$PREFIX/magnetk" install || echo "(link handler not registered)"
fi

if [ "$ROLE" = client ]; then
  mkdir -p "$HOME/.magnetk"
  [ -f "$HOME/.magnetk/settings.conf" ] || "$PREFIX/magnetk" config init "$HOME/.magnetk/settings.conf"
  echo
  echo "Done. Set RELAYS (and TOKEN) in ~/.magnetk/settings.conf, then e.g.:"
  echo "  magnetk chat <node>            magnetk open 'magnetk://<node>@<relay>/chat'"
  exit 0
fi

# ----- servers: relay or seed -----
if [ "$(id -u)" != 0 ]; then
  echo "--$ROLE installs a system service: run with sudo" >&2
  exit 1
fi
if [ "$(uname)" != Linux ] || ! command -v systemctl >/dev/null; then
  echo "no systemd here: run it yourself, e.g.  $PREFIX/magnetk $ROLE -config /etc/magnetk/settings.conf"
  exit 0
fi
id magnetk >/dev/null 2>&1 || useradd -r -s /usr/sbin/nologin -d /var/lib/magnetk magnetk
mkdir -p /etc/magnetk /var/lib/magnetk
chown magnetk: /var/lib/magnetk
[ -f /etc/magnetk/settings.conf ] || "$PREFIX/magnetk" config init /etc/magnetk/settings.conf

if [ "$ROLE" = relay ]; then
  EXEC="$PREFIX/magnetk relay -config /etc/magnetk/settings.conf -http 127.0.0.1:8090"
  UNIT=magnetk-relay
else
  SEED_DIR="$(cd "$SEED_DIR" && pwd)"
  EXEC="/bin/sh -c 'exec $PREFIX/magnetk share -config /etc/magnetk/settings.conf -direct \"$SEED_DIR\"/*'"
  UNIT=magnetk-seed
fi
cat >/etc/systemd/system/$UNIT.service <<EOF
[Unit]
Description=Magnetk $ROLE
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$EXEC
WorkingDirectory=/var/lib/magnetk
Restart=always
RestartSec=2
User=magnetk
LimitNOFILE=1048576
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now "$UNIT"
echo
echo "Done: $UNIT is running (journalctl -u $UNIT -f). Settings: /etc/magnetk/settings.conf"
[ "$ROLE" = relay ] && echo "Open UDP+TCP 4002 (and TCP 4003 for v1 clients): sudo ufw allow 4002 && sudo ufw allow 4003/tcp"
echo "Addresses of nodes on this relay look like: magnetk://<node>@$(hostname -f 2>/dev/null || hostname):4002"
