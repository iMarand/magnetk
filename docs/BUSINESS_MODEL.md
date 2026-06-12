# Magnetk — Business Model & Economics Plan

## Core Concept
Magnetk becomes a self-sustaining decentralized file distribution network where seeders earn money for sharing, downloaders pay tiny fees, and the protocol runs itself with no central company managing servers or money.

---

## Who Pays What

| Person | Pays | Earns |
|---|---|---|
| Downloader | Small fee per file (VISA or crypto) | Nothing, gets the file |
| Seeder | Nothing | Fee from downloaders |
| Relay operator | Their server costs | Fee from users who needed relay |
| Developer (you) | Nothing after deploying contract | Automatic protocol cut % |

---

## Payment Flow

```
Downloader pays fee (VISA or crypto)
         ↓
Smart contract holds payment in escrow
         ↓
Seeder delivers file successfully
         ↓
Smart contract releases payment to seeder automatically
         ↓
Protocol fee (2-5%) goes to developer wallet automatically
```

No manual work. No invoices. No bank. Runs forever on its own.

---

## VISA / Non-Crypto Users

Users who have no crypto wallet pay with their normal VISA or debit card. A payment gateway sits between them and the blockchain — invisible to the user.

```
User enters VISA card
         ↓
Payment gateway (MoonPay / Stripe Crypto / Transak) receives payment
         ↓
Gateway converts to crypto automatically
         ↓
Pays smart contract on user's behalf
         ↓
User gets file — never touches crypto at all
```

### User experience feels like:
- Enter card number
- Pay $0.50 for this file
- Download starts immediately

Crypto happens completely in the background. No wallet needed, no setup, no confusion.

### Recommended Gateways to Integrate:
- **MoonPay** — most widely used, easy API
- **Transak** — good global coverage
- **Stripe Crypto** — familiar to developers

---

## Seeder Incentive Model

Seeders are the backbone of the network. They earn money just by leaving files shared — the more they seed, the more they earn.

```
Seeder shares 50GB game file
         ↓
1000 people download it
         ↓
Each pays $0.10
         ↓
Seeder earns $95 (after 5% protocol fee)
         ↓
Seeder keeps sharing because it pays
```

This creates a self-growing network — more seeders join because there is money in it, which means faster downloads for everyone, which attracts more users.

---

## Large Files & Games Use Case

Game developers and content creators currently pay expensive CDN fees to distribute large files. With Magnetk they seed once and the community takes over distribution — and gets paid for it.

**Example: 50GB game distributed via Magnetk**

| Method | Developer Cost | Download Speed |
|---|---|---|
| Traditional CDN | Hundreds per TB | Limited by CDN |
| Magnetk | Near zero | Faster as more people seed |

Developer publishes magnet link → community seeds → everyone who downloads pays tiny fee → seeders earn → more seeders join → speeds increase automatically with popularity.

---

## Relay Operator Incentive

Relay operators run servers and earn fees from users who needed relay assistance (NAT issues, offline buffering). This makes the relay network self-growing — operators join because they earn, so more relays appear automatically as network grows.

```
User behind NAT cannot connect directly
         ↓
Relay assists the connection
         ↓
Smart contract charges small relay fee
         ↓
Fee goes directly to relay operator
         ↓
More operators join to earn → network grows itself
```

---

## Developer Revenue (Protocol Fee)

Smart contract automatically takes 2-5% of every transaction across the entire network. Goes to your wallet automatically, forever, with no manual work.

At scale this becomes significant passive income with zero running costs on your end since blockchain and peers do all the work.

---

## Three Network Modes

### Mode 1 — Free (DHT Pure P2P)
Peers connect directly via DHT. No relay, no blockchain, no fees. Works for users who just want simple free sharing like current Magnetk. Seeders share out of goodwill.

### Mode 2 — Assisted (Relay Network)
When direct connection fails, relay steps in and buffers. Small relay fee charged. Relay operators earn. Good for users behind strict NAT or firewalls.

### Mode 3 — Paid (Blockchain + Smart Contract)
Full payment system. Seeders earn per download. VISA or crypto accepted. Smart contract handles everything automatically. Best for premium content, games, large files.

Users can choose which mode fits them. All three can coexist on the same network.

---

## Enterprise Partnership Model (e.g. Rockstar / GTA 6)

### Two Separate Payments — Never Mixed:

When a publisher like Rockstar uses Magnetk, there are two completely separate payment layers that never interfere with each other.

```
Buyer pays $300 → goes to Rockstar (game license fee)
Buyer pays $0.50 → goes to seeders (delivery/bandwidth fee)
```

Rockstar sets their own price. Magnetk handles the delivery fee separately. Seeders never touch Rockstar's money and never need to pay Rockstar anything — they are bandwidth providers, not resellers.

### Full Enterprise Flow:
```
1. Buyer pays $300 to Rockstar smart contract
2. Contract verifies payment → releases encrypted magnetk link to buyer's wallet only
3. Buyer starts download → pulls chunks from multiple seeders via QUIC
4. Each chunk delivery is cryptographically signed by the seeder
5. Download completes → contract counts each seeder's chunk contributions
6. Delivery fee split proportionally among all seeders automatically
7. Magnetk protocol takes its percentage automatically
8. Rockstar keeps their $300, seeders earn delivery fee, you earn protocol fee
```

### Do Seeders Pay Rockstar?
No. Seeders are like delivery drivers — they move the data, they do not sell it. The buyer already paid Rockstar for the license. Think of it exactly like this:

```
Amazon delivery model:
You pay Amazon for the product   →   Rockstar gets $300
Amazon pays the delivery driver  →   Seeder gets bandwidth fee
Driver never pays Amazon         →   Seeder never pays Rockstar
```

### Multi-Seeder Chunk Accounting:
When a buyer downloads from multiple seeders simultaneously, each seeder gets paid proportionally to how many chunks they delivered.

```
File = 100 chunks total

Seeder A delivered 60 chunks → earns 60% of delivery fee
Seeder B delivered 30 chunks → earns 30% of delivery fee
Seeder C delivered 10 chunks → earns 10% of delivery fee
```

Every chunk is cryptographically signed by the seeder who delivered it — proof on-chain. Smart contract counts all signed chunks per seeder at the end and splits automatically. Nobody can fake or inflate their chunk count.

### Why Rockstar Benefits:

| Without Magnetk | With Magnetk |
|---|---|
| Pays millions for CDN servers | Pays near zero, community seeds |
| Servers crash on launch day | Faster as more people seed simultaneously |
| Centralized — can be attacked | Distributed — no single point of failure |
| Fixed download speed | Gets faster as game gets more popular |

### Piracy Protection:
The magnetk link is encrypted and tied to the buyer's wallet address. Even if buyer shares the link, nobody else can use it — smart contract checks that the wallet requesting download is the same wallet that paid. Content is protected at the protocol level.

### Your Revenue From Enterprise Deals:
- Licensing or integration fee from Rockstar (one time or annual)
- Protocol percentage of every delivery fee across all downloads
- Other studios see it working → approach you → more deals follow

---

## Summary

- Downloaders pay → seeders earn → relay operators earn → you earn
- VISA users pay normally, crypto conversion is invisible to them
- No servers for you to run or pay for after deployment
- Network grows itself because operators and seeders are financially incentivized
- Works for games, large files, premium content, any file type
- Built on existing Magnetk infrastructure — blockchain and gateway layer added on top

---

*Created: February 2026 — Part of Magnetk upgrade planning*
