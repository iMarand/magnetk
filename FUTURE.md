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

## Large Scale Distribution — How Launch Day Actually Works

### The Problem With Few Seeders at Launch:

When a publisher like Rockstar launches GTA 6, the naive assumption is they need massive servers or thousands of seeders ready. Without this the network collapses.

```
Rockstar + 100 seeders vs 10 million people downloading 150GB each
= 1.5 Exabytes of data needed on day one
= Impossible for any fixed number of servers to handle
```

This is why even Steam and PlayStation Store struggle on big launch days — traditional servers get more overwhelmed as more users arrive.

---

### How Magnetk Solves This — Viral Distribution:

The key is that **every downloader automatically becomes a seeder** the moment they start receiving chunks. They do not wait until fully downloaded — they share chunks as they receive them in real time.

```
Hour 1 — Rockstar seeds to first 1,000 people
         ↓
Hour 2 — Those 1,000 each seed to 10 more = 10,000 downloading
         ↓
Hour 3 — 10,000 each seed to 10 more = 100,000 downloading
         ↓
Hour 6 — Millions seeding to millions simultaneously
         ↓
Rockstar servers barely needed anymore
Network speed INCREASES as more people join
```

This is the complete opposite of traditional servers — more demand creates more supply automatically.

---

### Smart Chunk Distribution From the Start:

Rockstar does not send the same chunks to everyone in the first wave. They deliberately send different chunks to different people so the full file is spread across the network immediately.

```
Person A receives chunks 1–50
Person B receives chunks 51–100
         ↓
Immediately Person A and B can serve each other the full file
Without either having the complete file yet
         ↓
Network coverage grows exponentially from minute one
```

---

### Seeder CPU and Bandwidth Reality:

Seeding is extremely light on CPU — it is mostly a network task of reading chunks from disk and sending them. A normal home computer handles this easily.

```
Normal home upload speed = 10–50 Mbps
Serving one downloader = 2–5 Mbps
One home seeder comfortably serves 5–20 people simultaneously
         ↓
10,000 early seeders × 10 people each
= 100,000 simultaneous full-speed downloads from launch minute one
```

---

### Incentivizing Early Seeders:

Smart contract rewards early seeders with a higher delivery fee percentage because they carry the heaviest load when seeders are few. As more seeders join the bonus decreases naturally.

```
First hour seeders → earn 3x normal delivery fee
First day seeders  → earn 2x normal delivery fee
After launch week  → earn standard delivery fee
```

This financially motivates people to seed early and stay seeding, solving the cold start problem automatically.

---

### Rockstar's Role in This Model:

Rockstar only needs to seed to the first wave well — maybe 10,000 to 50,000 initial downloaders. After that the network takes over completely. Their infrastructure cost drops dramatically within hours while everyone else gets faster speeds.

| Time | Rockstar Load | Network Speed |
|---|---|---|
| Launch minute | High — serving first wave | Moderate |
| Hour 3 | Medium — supplementing | Good |
| Hour 6 | Low — mostly idle | Fast |
| Day 2 | Near zero | Fastest — millions seeding |

---

## Large Scale Distribution — How Magnetk Handles Millions of Users

### The Real Problem at Launch Day:

When a massive game like GTA 6 launches, millions of people want it simultaneously. Traditional servers collapse under this pressure.

```
GTA 6 = 150GB per person
10 million people downloading day one
= 1.5 Exabytes of data to serve
= No single company's servers can handle this
```

Even Steam struggles on major launch days. A publisher like Rockstar seeding alone with even a few hundred seeders cannot serve millions of people at acceptable speeds.

### How Magnetk Solves This — Viral Distribution:

The key is that every downloader automatically becomes a seeder the moment they start receiving chunks. They do not wait until fully downloaded — they share chunks as they receive them. This creates a self-amplifying network effect.

```
Hour 1 — Launch:
Rockstar seeds first wave to 10,000 people
         ↓
Hour 2:
Those 10,000 each seed to 10 more = 100,000 people downloading
         ↓
Hour 3:
100,000 each seed to 10 more = 1,000,000 people downloading
         ↓
Hour 6:
Millions seeding to millions simultaneously
Rockstar servers barely needed anymore
Network speed INCREASES as more people join
```

This is the complete opposite of traditional servers — more demand creates more supply automatically.

### Smart Chunk Distribution at Launch:

Rockstar does not send the same chunks to everyone in the first wave. Instead Magnetk deliberately distributes different chunks to different people so the full file is spread across the network immediately.

```
Person A receives chunks 1-50
Person B receives chunks 51-100
         ↓
Immediately A and B can seed the complete file between them
Without waiting for either to finish downloading
         ↓
Coverage spreads exponentially from minute one
```

### CPU and Bandwidth Reality for Seeders:

Seeding is very light on CPU — it is mostly a network task of reading chunks from disk and sending them. A normal home computer handles this easily.

```
Normal home upload speed = 10-50 Mbps
Serving one downloader = 2-5 Mbps used
One home seeder comfortably serves 5-20 people simultaneously
         ↓
10,000 initial seeders × 10 people each
= 100,000 simultaneous downloads at full speed from minute one
```

### Incentivized Early Seeding:

Smart contract rewards early seeders with a higher delivery fee percentage because they carry the heaviest load when seeders are few. This encourages people to seed generously from launch and not disconnect.

```
Launch day seeder earns 1.5× normal delivery fee
Week 2 seeder earns standard delivery fee
         ↓
Financial incentive to join early → network bootstraps itself fast
```

### What Rockstar Actually Needs to Run:

Instead of massive CDN infrastructure serving everyone, Rockstar only needs to seed the first wave well — perhaps 10,000-50,000 initial connections. After a few hours the community carries the entire load. Their server costs drop dramatically while download speeds for everyone improve.

```
Traditional launch:      More users = slower speeds = costs explode
Magnetk launch:          More users = faster speeds = costs drop
```

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