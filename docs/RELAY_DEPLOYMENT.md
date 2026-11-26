# Running relays (one VPS, two VPS, or more)

A relay is one binary (`magnetk relay`). Nodes connect *out* to it, so
they work behind any NAT, firewall or mobile network. Peer-to-peer data
paths are used automatically when nodes can reach each other; the relay
carries the rest.

| Setup | When | What you get |
|---|---|---|
| 1 relay | most apps, one region | every feature; single point of failure |
| 2 relays, federated | production | failover, nodes on either relay talk to each other, lower latency per region |
| N relays, full mesh | several regions | same, each relay links to every other relay |

Sizing: the relay is mostly I/O. A 1 vCPU / 1 GB VPS handles thousands of
nodes; relayed pipe throughput is normally limited by the VPS network
link, not by CPU (see [BENCHMARKS.md](BENCHMARKS.md)). Memory is bounded by
`MAX_QUEUE_PER_NODE`, `OFFLINE_MAX_BYTES` and `MAX_PIPES` × 2 × 256 KiB.

## 1. Build the binary

On the VPS (recommended, gives QUIC):

```bash
git clone https://github.com/iMarand/magnetk.git && cd magnetk
./scripts/build.sh            # builds MsQuic (needs cmake, pwsh, perl, make) + magnetk
```

Without MsQuic — a static, pure-Go binary that speaks TCP+TLS 1.3 only
(clients in `auto` mode fall back to TCP automatically; you lose QUIC
datagrams and connection migration, not features):

```bash
./scripts/build.sh tcp        # or, from any OS:
cd go && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags nomsquic -o magnetk ./cmd/magnetk
```

## 2. Open the ports

| Port | Protocol | Purpose |
|---|---|---|
| 4002 | UDP | QUIC (nodes and relay links) |
| 4002 | TCP | TCP+TLS fallback (and v1 clients sniffed on the same port) |
| 4003 | TCP | v1 / JavaScript SDK (optional, `LEGACY_PORT=off` disables) |

```bash
sudo ufw allow 4002/udp && sudo ufw allow 4002/tcp && sudo ufw allow 4003/tcp
```

Also open them in the provider's firewall / security group if it has one.

## 3. One relay

```bash
./build/bin/magnetk relay -tokens "$(openssl rand -hex 16)" -http 127.0.0.1:8090
```

Nodes: `RELAYS=vps1.example.com:4002` and `TOKEN=<that token>` (or
`-relay`/`-token` flags, or the SDK options). Leave `-tokens` empty only for
an open test relay.

## 4. Two relays (federation)

Use the same secret on both and point each at the other (pointing only one
at the other also works; listing both is more robust):

```bash
# VPS 1 (e.g. 203.0.113.10)
magnetk relay -id eu-1 -peers 198.51.100.20:4002 -secret "$CLUSTER_SECRET" -tokens "$TOKENS"
# VPS 2 (e.g. 198.51.100.20)
magnetk relay -id us-1 -peers 203.0.113.10:4002 -secret "$CLUSTER_SECRET" -tokens "$TOKENS"
```

Nodes list both: `RELAYS=203.0.113.10:4002,198.51.100.20:4002` (closest
first). They connect to the first that answers within 400 ms, otherwise the
next, and fail over when one goes down; shared files are re-registered and
subscriptions restored automatically. A node on `eu-1` can message,
subscribe to, request from, open pipes to and download from a node on
`us-1`. Share links carry every relay in `LINK_RELAYS` (default: `RELAYS`).

Check the federation: `curl 127.0.0.1:8090/stats` on each relay shows
`links`, `nodes` and `remote_nodes`.

## 5. As a service (systemd)

Quickest: `sudo scripts/install.sh --relay` (or `--seed /srv/files`) installs
`/opt/magnetk`, writes `/etc/magnetk/settings.conf` and the unit below and
starts it. On a desktop machine the same script also registers `magnetk://`
links ([LINKS.md](LINKS.md)). By hand:


`/etc/magnetk/settings.conf` (generate with `magnetk config init /etc/magnetk/settings.conf`):

```ini
RELAY_ID=eu-1
RELAY_PORT=4002
AUTH_TOKENS=<token1>,<token2>
RELAY_PEERS=198.51.100.20:4002
CLUSTER_SECRET=<long random secret>
# Optional real certificate (then clients can use NO_CERT_VALIDATION=false):
# TLS_CERT_FILE=/etc/letsencrypt/live/relay.example.com/fullchain.pem
# TLS_KEY_FILE=/etc/letsencrypt/live/relay.example.com/privkey.pem
MAX_PROCS=0
MEMORY_LIMIT_MB=512
```

`/etc/systemd/system/magnetk-relay.service`:

```ini
[Unit]
Description=Magnetk relay
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/opt/magnetk/magnetk relay -config /etc/magnetk/settings.conf -http 127.0.0.1:8090
WorkingDirectory=/opt/magnetk
Restart=always
RestartSec=2
User=magnetk
LimitNOFILE=1048576
# UDP buffers for QUIC at high rates (also set net.core.rmem_max/wmem_max)
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
```

```bash
sudo useradd -r -s /usr/sbin/nologin magnetk
sudo systemctl daemon-reload && sudo systemctl enable --now magnetk-relay
journalctl -u magnetk-relay -f
```

For high QUIC throughput raise the UDP buffers:

```bash
sudo sysctl -w net.core.rmem_max=7500000 net.core.wmem_max=7500000
```

## 6. Monitoring

`-http 127.0.0.1:8090` serves:

* `/stats` — JSON: nodes, remote nodes, links, subscriptions, files, active
  pipes, offline queue, counters, Go runtime (heap, goroutines, GC);
* `/metrics` — the same as Prometheus text;
* `/healthz` — `ok`.

Keep it on localhost (or behind a reverse proxy with auth).

## 7. Resource limits

All in `settings.conf` (documented in the generated file):

| Setting | Default | Effect |
|---|---|---|
| `MAX_NODES` | 100000 | sessions per relay |
| `MAX_MESSAGE_SIZE` | 16MB | largest frame |
| `MAX_QUEUE_PER_NODE` | 32MB | outbound buffer per node |
| `SLOW_CONSUMER` | drop | `drop` messages or `disconnect` a node that cannot keep up |
| `MAX_PIPES` | 10000 | concurrent relayed pipes |
| `OFFLINE_MAX_MESSAGES` / `OFFLINE_MAX_BYTES` / `OFFLINE_TTL` | 1000 / 256MB / 1h | store-and-forward for offline nodes |
| `SESSION_TIMEOUT` | 45s | drop silent nodes/links |
| `RATE_LIMIT` | 0 | messages/s per node |
| `MAX_PROCS`, `MEMORY_LIMIT_MB` | 0 | CPU cores and soft heap limit |
| `EXECUTION_PROFILE`, `CONGESTION_CONTROL` | max_throughput, cubic | QUIC tuning |

Every key can also be set as an environment variable `MAGNETK_<KEY>`.

## 8. Testing a deployment from a phone

Run `magnetk diag host -listen "" -relay vps1:4002 -token T` on any PC (it
joins the relay as the test host), install the Android test app, enter
`vps1:4002` and the token, and run the tests on Wi-Fi and on mobile data.
See [FIELD_TEST.md](FIELD_TEST.md).
