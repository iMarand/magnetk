# Magnetk Run Guide

Step-by-step guide to running the Magnetk P2P network.

---

## Port Layout

| Port | Protocol | Service | Who Runs It |
|------|----------|---------|-------------|
| **4001** | TCP | Relay (LibP2P) | VPS only |
| **4002** | UDP + TCP | Seed data transfer (QUIC + TCP fallback) | VPS (seeder) |
| **4003** | TCP | Relay (JS fallback) | VPS only (auto, part of relay) |

> [!IMPORTANT]
> Port **4002** is for seeds only. Port **4003** is for the relay's JS-compatible TCP listener. **Never run a seed on port 4003** — it conflicts with the relay.

---

## `ADVERTISED_ADDRESS` Explained

When a seed publishes a file, it tells the relay/DHT: *"Connect to me at THIS_IP:THIS_PORT to download."*

| Scenario | Value | Why |
|----------|-------|-----|
| **VPS (seeding)** | `ADVERTISED_ADDRESS=69.169.109.243` | Your VPS public IP. Required because STUN may return wrong IP behind NAT. |
| **Client (downloading)** | `ADVERTISED_ADDRESS=` (empty) | Clients don't advertise — they connect to seeds, not the other way around. |

If left empty on a VPS, the seed uses STUN auto-discovery which may return an internal/NAT IP (e.g., `38.4.10.0`) that nobody can reach.

---

## VPS Setup

### 1. Config: `config/settings.conf`

This config runs on the VPS where your **relay** and **seed** live:

```ini
# ===== CRITICAL FOR VPS =====
ADVERTISED_ADDRESS=69.169.109.243    # ← Your VPS public IP
TRANSPORT_PORT=4002                   # ← Seed data port (not 4003!)
DISCOVERY_PORT=4001                   # ← LibP2P discovery

# ===== Network =====
BIND_ADDRESS=0.0.0.0
LOCALHOST_MODE=false

# ===== Relay =====
RELAY_ADDRESS=0.0.0.0:4001

# ===== TLS =====
TLS_CERT_FILE=
TLS_KEY_FILE=
NO_CERT_VALIDATION=true

# ===== DHT =====
DHT_ENABLED=false
DHT_MODE=auto
DHT_BOOTSTRAP_PEERS=/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN,/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa,/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb,/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt,/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ

# ===== Chunking =====
CHUNK_SIZE_MB=1
MAX_PARALLEL_CHUNKS=10

# ===== Performance =====
MAX_CONNECTIONS=1000
SEND_BUFFER_SIZE_MB=4
RECV_BUFFER_SIZE_MB=4
```

### 2. Start the Relay (in tmux)

```bash
tmux new -s relay
cd ~/Desktop/Magnetk/magnetk
./bin/relay -config ./config/settings.conf
# Ctrl+B, then D to detach
```

The relay listens on:
- **Port 4001** — LibP2P discovery (Go/Python peers connect here)
- **Port 4003** — TCP fallback (JS clients connect here)

### 3. Verify relay is running

```bash
ss -tlnp | grep -E "4001|4003"
# Should show both ports listening
```

### 4. Share a file (Seed)

#### Option A: Using Go binary directly
```bash
./bin/seed \
  -file ./path/to/file.txt \
  -port 4002 \
  -config ./config/settings.conf \
  -relay /ip4/69.169.109.243/tcp/4001/p2p/<RELAY_PEER_ID> \
  -dht
```

#### Option B: Using Python SDK
```bash
cd sdk/python
python3 examples/share.py
```

#### Option C: Using JS SDK
```bash
cd sdk/js/magnetk
node share.js
```

### 5. Verify seed is running

```bash
ss -ulnp | grep 4002   # QUIC (UDP)
ss -tlnp | grep 4002   # TCP fallback
# Both should show the seed process
```

> [!CAUTION]
> If you see `address already in use` on port 4002, another process is using it. Kill it with:
> ```bash
> kill $(lsof -t -i :4002)
> ```

---

## Client Setup (Downloading)

### Config: `config/settings.conf` (Client side)

On client machines, ADVERTISED_ADDRESS should be **empty**:

```ini
BIND_ADDRESS=0.0.0.0
DISCOVERY_PORT=4001
TRANSPORT_PORT=4002
LOCALHOST_MODE=false

# Leave empty on clients — only VPS seeds need this
ADVERTISED_ADDRESS=

# Chunking
CHUNK_SIZE_MB=1
MAX_PARALLEL_CHUNKS=10

# DHT
DHT_ENABLED=false
DHT_MODE=auto
DHT_BOOTSTRAP_PEERS=/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN,/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa,/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb,/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt,/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ
DISCOVERY_PRIORITY=dht,relay,direct
```

### Download with JS SDK

```bash
node download.js "magnetk:?dn=file.txt&relay=...&xt=urn:sha256:..."
```

### Download with Python SDK

```bash
python download.py "magnetk:?dn=file.txt&relay=...&xt=urn:sha256:..."
```

---

## Troubleshooting

### "Failed to start TCP listener: address already in use"
Another process holds the port. Find and kill it:
```bash
ss -tlnp | grep <PORT>
kill <PID>
```

### "Failed to parse relay peer info: invalid p2p multiaddr"
The relay address is missing the Peer ID. You need the full format:
```
/ip4/69.169.109.243/tcp/4001/p2p/12D3KooW...
```
Both JS and Python SDKs auto-fetch the Peer ID from port 4003.

### "STUN discovered: 38.4.10.0" (wrong IP)
Your VPS is behind NAT. Set `ADVERTISED_ADDRESS=69.169.109.243` in settings.conf.

### "transport: timeout" on download
- UDP port 4002 might be blocked by your VPS provider's network firewall (separate from `ufw`)
- Check your VPS provider's dashboard for firewall rules
- The JS SDK uses TCP fallback and may work even when QUIC/UDP is blocked

### "stream closed (EOF)" on download
The peer connected to the **relay** port instead of the **seed** port. Check that:
- `TRANSPORT_PORT=4002` in config (not 4003 or 4005)
- The seed is actually running on port 4002
- No other process occupies port 4002

---

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────┐
│                        VPS (69.169.109.243)                 │
│                                                             │
│  ┌─────────────────┐         ┌────────────────────┐        │
│  │     RELAY        │         │       SEED          │        │
│  │                  │         │                     │        │
│  │  TCP:4001 LibP2P │◄───────►│  UDP:4002 QUIC      │        │
│  │  TCP:4003 JS TCP │         │  TCP:4002 Fallback   │        │
│  └─────────────────┘         └────────────────────┘        │
│                                                             │
└──────────────┬──────────────────────────┬───────────────────┘
               │                          │
          Discovery                  Data Transfer
        (port 4001/4003)            (port 4002)
               │                          │
┌──────────────▼──────────────────────────▼───────────────────┐
│                     CLIENT (your PC)                         │
│                                                              │
│  1. Asks relay "who has file X?"  →  relay:4003 (JS)         │
│  2. Relay says "seed at 69.169.109.243:4002"                 │
│  3. Client connects to seed:4002 for chunks                  │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```
