# Magnetk + Blockchain Integration Plan

## What is Magnetk?
Magnetk is a peer-to-peer file transfer network built on libp2p + QUIC. It transfers files directly between peers using magnet links — no central storage server, with automatic resume, relay-assisted delivery, and SDKs for Go, JavaScript, Python, and C++.

## What is Blockchain?
A permanent, tamper-proof public ledger. Smart contracts live on it and run automatically — no server, no middleman, forever.

---

## Why Combine Them?

| Layer | Magnetk | Blockchain |
|---|---|---|
| Strength | Fast file transfer (QUIC/UDP) | Trust, payment, ownership |
| Weakness | No payment, no permanent record | Too slow for raw file data |

**Together they cover each other's weaknesses perfectly.**

---

## How They Work Together

```
Magnetk  =  The Highway      (moves files fast between peers)
Blockchain = The Law & Bank  (proves ownership, handles payment)
```

### Flow:
```
1. Sender seeds file via Magnetk → gets magnetk:? link
          ↓
2. Sender writes magnet link to smart contract on blockchain
          ↓
3. Anyone reads magnet link from blockchain (permanent, public)
          ↓
4. Downloader uses magnet link → downloads via Magnetk QUIC transport
          ↓
5. Smart contract automatically pays sender when download verified
```

---

## New Features: Blockchain + Magnetk Combined

### 1. Streaming Before Full Download
Nobody does this with P2P + blockchain. User pays → smart contract unlocks → Magnetk streams chunks in order via QUIC → user watches/uses file while still downloading. Like Netflix but fully decentralized.

### 2. Time-Limited Access
Smart contract gives magnet link access that expires after X hours/days. Pay for 24hr access, link stops working after. A rental system on blockchain — Filecoin and BTT have no concept of this.

### 3. Private Encrypted Sharing
File encrypted before seeding. Smart contract holds decryption key. Only released to wallet that paid. Magnetk transfers encrypted file, blockchain releases key separately. Nobody else can read it even if they intercept the transfer.

### 4. Split Revenue Between Multiple Seeders
Smart contract automatically splits payment between everyone who helped seed the file — proportional to how many chunks they served. Neither Filecoin nor BTT split fairly per chunk contribution.

### 5. File Versioning on Blockchain
Publisher updates a file → new magnet link → blockchain records version history. Downloaders always know which version is latest and who published each version. Like Git but decentralized.

### 6. Reputation System
Seeders build on-chain reputation score based on successful deliveries. Bad seeders get flagged automatically by smart contract. Neither Filecoin nor BTT have a proper on-chain reputation system built in.

---

## New Features: Magnetk Standalone (No Blockchain Needed)

### Transfer Improvements

**Adaptive Chunk Sizing** — Currently chunks are fixed size. Make them adapt based on network speed — bigger chunks on fast connections, smaller on slow/unstable. Faster overall transfers.

**Multi-Source Downloading** — Download same file from multiple seeders simultaneously, each serving different chunks. Like how BitTorrent works but over QUIC — significantly faster for popular files.

**Partial File Requests** — Request only specific byte ranges of a file. Useful for previewing large files or resuming at exact position. Currently resume works but partial selection doesn't exist.

**Compression On The Fly** — Compress chunks before sending, decompress on arrival. Saves bandwidth especially for text-based files. Happens transparently without user doing anything.

### Connectivity Improvements

**DHT (Distributed Hash Table)** — Remove reliance on a single relay for peer discovery. Peers find each other through a distributed network — no relay needed, truly decentralized. Current relay is a semi-centralized point of failure.

**NAT Hole Punching** — Automatically punch through NAT without needing port forwarding. Right now seeders need port forwarding — this would eliminate that requirement completely.

**WebRTC Support** — Allow browsers to directly participate as peers without Node.js. Someone opens a webpage and can seed or download — no installation needed.

### SDK Improvements

**Mobile SDKs (Android/iOS)** — Currently Go, JS, Python, C++ — no mobile. A React Native or Flutter SDK would open Magnetk to mobile apps entirely.

**Browser SDK** — Pure browser JavaScript SDK using WebRTC. No Node.js required, works straight in a webpage.

### User Features

**Magnet Link Collections** — One magnet link that points to a folder/collection of files rather than a single file. Download entire folders in one go.

**Transfer Speed Limits** — Let seeders cap how much bandwidth Magnetk uses so it doesn't consume their entire connection while seeding.

**Seeder Discovery Without Relay** — If relay goes down, peers that already know each other can still discover new peers through each other — mesh-style discovery.

---

## Architecture

```
┌─────────────────────────────────────────┐
│           Smart Contract                 │
│  - Stores magnetk:? links               │
│  - Handles ETH payments                 │
│  - Controls access per file hash        │
│  - Records ownership (sha256 → owner)   │
└──────────────────┬──────────────────────┘
                   │ reads/writes magnet links
┌──────────────────▼──────────────────────┐
│               Magnetk                    │
│  - QUIC (UDP) for Go/Python/C++ peers   │
│  - TCP fallback for JS SDK              │
│  - Relay buffers chunks offline         │
│  - Resume support on interruption       │
└─────────────────────────────────────────┘
```

---

## Tech Stack Needed

- **Magnetk** — file transfer layer (already built)
- **Solidity** — smart contract (already learned)
- **Ethers.js** — talk to blockchain from JS SDK
- **Sepolia Testnet** — testing (already set up)
- **MetaMask** — wallet for signing transactions

---

## MVP (Simplest Start)

1. Deploy smart contract that stores magnet links mapped to file sha256 hash
2. Seeder writes their `magnetk:?` link to contract after seeding
3. Downloader reads magnet link from contract
4. Downloads file via Magnetk
5. Done — no server, no middleman

**Add payment layer after basic flow works.**

---

## Comparison to Similar Projects

| Project | File Layer | Blockchain | vs Magnetk combo |
|---|---|---|---|
| Filecoin | IPFS | Filecoin chain | Magnetk faster via QUIC |
| BitTorrent Token | BitTorrent | TRON | Similar concept |
| Storj | P2P storage | Ethereum | Similar concept |

---

## Priority Roadmap

| Priority | Feature | Impact |
|---|---|---|
| 🔴 High | DHT + NAT hole punching | Eliminates relay dependency |
| 🔴 High | Multi-source downloading | Massive speed improvement |
| 🟡 Medium | Streaming + blockchain payment | Unique, nobody has this |
| 🟡 Medium | Encrypted sharing + key on blockchain | Strong privacy feature |
| 🟢 Later | Mobile SDKs | Expands reach to mobile |
| 🟢 Later | Time-limited access | Monetization feature |

---

*Created: February 2026 — Stack: Magnetk (libp2p+QUIC) + Ethereum Solidity + Ethers.js*