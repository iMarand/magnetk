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

## What This Enables

### 1. Decentralized File Marketplace
Sellers list magnet links + price on blockchain. Buyers pay smart contract → get magnet link → download via Magnetk. No middleman, automatic payment.

### 2. Pay-Per-Download
Smart contract holds payment in escrow. Released to seeder only when download completes. No trust required between parties.

### 3. Proof of File Existence
File hash stored on blockchain forever. Proves file existed at a certain time. Cannot be tampered with or deleted.

### 4. Unstoppable File Sharing
Magnet links on blockchain = permanent, uncensorable. File transferred via Magnetk = no central server to shut down.

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

*Created: February 2026 — Stack: Magnetk (libp2p+QUIC) + Ethereum Solidity + Ethers.js*