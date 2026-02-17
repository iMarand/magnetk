# Magnetk P2P Network - Architecture

## System Overview

**Magnetk** is a hybrid P2P file transfer network with three node types:
- **Relay Nodes** (your infrastructure) - Network backbone
- **Seed Nodes** (third-party VPS) - File hosts using Magnetk SDK
- **Peer Clients** (end users) - Download files via magnetk links

---

## Network Topology

```mermaid
graph TB
    subgraph "Your Infrastructure"
        R1[Relay 1]
        R2[Relay 2]
        R1 <--> R2
    end
    
    subgraph "Third-Party Seeds"
        S1[Seed A<br/>movie.mp4]
        S2[Seed B<br/>game.zip]
        S3[Seed C<br/>data.tar]
    end
    
    subgraph "End Users"
        P1[Peer 1]
        P2[Peer 2]
        P3[Peer 3]
    end
    
    subgraph "DHT Network"
        D1((DHT Node 1))
        D2((DHT Node 2))
        D3((DHT Node 3))
        D1 -.- D2
        D2 -.- D3
        D3 -.- S1
        D1 -.- P1
    end
    
    S1 --> R1
    S2 --> R1
    S3 --> R2
    
    R1 --> P1
    R1 --> P2
    R2 --> P3
    
    %% DHT Connections (Discovery)
    S1 -.-> D3
    P1 -.-> D1
```

---

## Node Type Comparison

| Aspect | Relay | Seed | Peer |
|--------|-------|------|------|
| **Operator** | You | VPS owners | End users |
| **Purpose** | Route traffic | Host files | Download |
| **SDK** | No (core binary) | Yes (SDK) | Yes (SDK) |
| **Storage** | Buffer only | Full files | Downloads |
| **Revenue** | All traffic | None (uses your relay) | None |

---

## Layer Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                   APPLICATION LAYER                          │
├─────────────────────────────────────────────────────────────┤
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐      │
│  │  Magnetk SDK │  │ Magnetk Link │  │    Chunk     │      │
│  │   (Go/Py/JS) │  │   Parser     │  │   Manager    │      │
│  └──────────────┘  └──────────────┘  └──────────────┘      │
├─────────────────────────────────────────────────────────────┤
│                   PROTOCOL LAYER (Go)                        │
├─────────────────────────────────────────────────────────────┤
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐      │
│  │   libp2p     │  │  Discovery   │  │   Registry   │      │
│  │    Host      │  │   (DHT +     │  │  (on Relay)  │      │
│  │              │  │    Relay)    │  │              │      │
│  └──────────────┘  └──────────────┘  └──────────────┘      │
├─────────────────────────────────────────────────────────────┤
│                   TRANSPORT LAYER (C++)                      │
├─────────────────────────────────────────────────────────────┤
│  ┌──────────────────────────────────────────────────┐      │
│  │              MsQuic (TLS 1.3 + QUIC)             │      │
│  └──────────────────────────────────────────────────┘      │
└─────────────────────────────────────────────────────────────┘
```

---

## Magnetk Link Structure

```
magnetk:?xt=urn:sha256:<HASH>&dn=<NAME>&xs=<SEED_ID>&relay=<RELAY>

┌────────────────────────────────────────────────────────────┐
│  magnetk:?xt=urn:sha256:a1b2c3d4...                       │
│          &dn=awesome-file.zip                              │
│          &xs=Qm1234567890abcdef...                        │
│          &relay=relay.magnetk.io:4001                      │
└────────────────────────────────────────────────────────────┘
        │           │           │              │
        ▼           ▼           ▼              ▼
    File Hash   Filename    Seed Peer     Required Relay
```

---

## Data Flow: Complete Download

```
                [SEED VPS]                    [YOUR RELAY]                    [PEER]
                    │                              │                             │
  1. PUBLISH ───────┼─────────────────────────────►│                             │
     {hash, name}   │                              │                             │
                    │◄─────────────────────────────┤                             │
                    │   MAGNETK_LINK              │                             │
                    │                              │                             │
                    │                              │◄────────────────────────────┤
                    │                              │   2. RESOLVE {magnetk_link} │
                    │                              │                             │
                    │                              │────────────────────────────►│
                    │                              │   SEED_INFO {addr}          │
                    │                              │                             │
                    │◄─────────────────────────────┼─────────────────────────────┤
                    │   3. CONNECT via relay       │                             │
                    │                              │                             │
                    │─────────────────────────────►│─────────────────────────────►│
                    │   4. MANIFEST                │                             │
                    │                              │                             │
                    │─────────────────────────────►│─────────────────────────────►│
                    │   5. CHUNKS                  │   (routed through relay)    │
                    │                              │                             │
```

---

## Peer Discovery & DHT

Magnetk uses a hybrid discovery system combining a **Kademlia DHT** (via libp2p) with **Centralized Relays**.

### 1. Kademlia DHT
- **Purpose**: Decentralized peer discovery without relying on a single relay.
- **Implementation**: `go-libp2p-kad-dht`
- **Keys**: SHA-256 hash of the file (multihash) -> CIDv1 (Raw codec).
- **Values**: Provider records (Peer IDs + Multiaddrs).
- **Bootstrap**: Nodes connect to public IPFS bootstrap nodes or custom Magnetk bootstrap nodes.

### 2. Relay Discovery
- **Purpose**: Fast, reliable discovery for managed infrastructure.
- **Implementation**: Custom protocol over libp2p streams.
- **Behavior**: Seeds push file manifests to relays; Peers query relays.

### 3. Discovery Priority
Configurable via `DISCOVERY_PRIORITY` in `settings.conf`:
1.  **DHT**: Query the decentralized network first.
2.  **Relay**: Fallback to the relay if DHT fails or returns no results.
3.  **Direct**: Connect directly if the peer address is known (e.g., from a magnet link with `xs=` parameter).


## Relay Registry Schema

```json
{
  "files": {
    "sha256:abc123...": {
      "name": "movie.mp4",
      "size": 4294967296,
      "chunk_count": 4096,
      "seeds": [
        {
          "peer_id": "Qm...",
          "address": "/ip4/1.2.3.4/udp/4001/quic",
          "last_seen": "2024-01-15T10:00:00Z",
          "bandwidth": "100mbps"
        }
      ],
      "downloads": 12450,
      "created": "2024-01-10T08:00:00Z"
    }
  }
}
```

---

## SDK Components

| SDK | Language | Use Case |
|-----|----------|----------|
| `magnetk-sdk-go` | Go | High-performance seeds, CLI tools |
| `magnetk-sdk-py` | Python | Scripts, automation, backend |
| `magnetk-sdk-js` | JavaScript | Browser downloads, Node.js servers |

---

## Security Model

| Layer | Mechanism |
|-------|-----------|
| **Transport** | QUIC + TLS 1.3 (E2E encrypted) |
| **Identity** | libp2p peer keys (ed25519) |
| **Integrity** | SHA-256 per chunk + file |
| **Routing** | All traffic via trusted relays |
| **Access** | Optional API keys for seeds |
