# Magnetk — Development Roadmap

**From single-file P2P to full communication middleware**

---

## Where You Are Right Now

Before the roadmap, this is an honest picture of what exists today:

- QUIC transport working between Go, Python, C++ peers
- TCP fallback for JavaScript SDK
- Single relay server for peer discovery and NAT assistance
- Magnet links with SHA-256 content addressing
- Single-file download: one seeder → one downloader
- Basic resume support on interruption
- Smart contract basics explored on Sepolia testnet

Everything from here forward is what you build next, in order. Each phase builds directly on the previous one. Do not skip phases — later phases depend on foundations laid earlier.

---

## Phase 1 — Solid Foundation

**Goal: make what you have actually reliable and ready to build on.**

You cannot build a payment system on top of a transport layer that has bugs. You cannot build multi-source downloading before single-source is rock solid. This phase is unglamorous but mandatory.

### Step 1.1 — Fix and harden the existing transport

Go through the existing Go, JS, and Python SDKs and make sure these work without failure:

- File download completes correctly 100% of the time on stable connections
- Resume works correctly after a crash or interruption at any point mid-download
- Chunk integrity verification (SHA-256 per chunk) catches any corrupted data
- Connection drops are detected and reconnected automatically
- Seeder disconnecting mid-download triggers a retry, not a crash

Write tests for every one of these scenarios. Run them until they pass consistently. This is not optional — every feature you build later depends on this being solid.

### Step 1.2 — Multiple seeders, single file

This is the first real upgrade from the current state. Right now one seeder serves one downloader. The upgrade: one file is downloaded simultaneously from multiple seeders, each serving different chunks.

What to implement:

- The downloader connects to all known seeders for a file at the same time
- Chunks are assigned to seeders so no two seeders send the same chunk
- If a seeder disappears mid-download, their assigned chunks are reassigned to remaining seeders
- Download speed is the combined upload speed of all seeders, not just one

This is the core P2P speed advantage. A file with 10 seeders downloads roughly 10x faster than from one seeder. This is the feature that makes everything else worth building.

### Step 1.3 — Adaptive chunk sizing

Currently chunks are fixed size. Make them adapt to network conditions:

- Fast, stable connection → larger chunks (less overhead per byte)
- Slow or unstable connection → smaller chunks (less wasted bandwidth on retries)
- Measure round-trip time per peer continuously and adjust chunk size per-peer independently

This makes every transfer faster without the user doing anything.

### Step 1.4 — NAT hole punching

Right now peers behind NAT need the relay to connect. NAT hole punching allows many of these peers to connect directly without the relay, reducing relay load and improving speed.

Implement STUN-based hole punching. When two peers both sit behind NAT, attempt a coordinated direct connection before falling back to relay. Most consumer NAT configurations (symmetric excluded) will succeed.

This does not replace the relay — it reduces how often the relay is needed.

### Step 1.5 — Proper DHT peer discovery

Right now peer discovery depends on the central relay server. Replace this with a proper Distributed Hash Table so peers find each other without any central point.

Implement Kademlia DHT (already available in LibP2P). When a peer wants to download a file, it queries the DHT with the file hash and gets back a list of peers currently seeding that hash. No central registry involved.

The relay stays as a connection assistant (NAT), but is no longer the only way peers know about each other.

**What you have at the end of Phase 1:** A reliable, fast, truly decentralized file transfer network. Multiple seeders, DHT discovery, NAT hole punching, adaptive chunks. This is already better than most existing P2P implementations.

---

## Phase 2 — The Payment Layer

**Goal: seeders earn money automatically. Downloaders pay automatically. No manual work.**

### Step 2.1 — Smart contract: basic escrow and release

Deploy a smart contract on Sepolia testnet (you have already started this) that does exactly this:

1. Downloader deposits payment into contract escrow before download starts
2. Download completes
3. Contract releases payment to seeder automatically
4. Contract takes protocol fee (2–5%) to developer wallet automatically

Start with crypto payment only (ETH or a stablecoin). No fiat yet. Test thoroughly on testnet before mainnet.

The contract needs to handle: partial delivery (seeder disconnects halfway), failed delivery (seeder never sends), and disputed delivery (seeder claims they sent, downloader claims they didn't receive).

For disputed delivery in v1: use a simple timeout. If the downloader does not confirm receipt within a window, the contract assumes failure and refunds. This is not perfect but it is safe and simple.

### Step 2.2 — Chunk-level payment accounting

Upgrade the contract to track delivery at the chunk level, not just the file level. Each chunk delivered by a seeder is cryptographically signed. The contract counts signed chunks per seeder and splits payment proportionally at the end.

```
File = 100 chunks

Seeder A delivered and signed 60 chunks → earns 60% of seeder fee
Seeder B delivered and signed 40 chunks → earns 40% of seeder fee
Protocol fee → developer wallet automatically
```

This makes multi-seeder payments fair and automatic. It also means partial delivery is handled correctly — if a seeder disappears after delivering half their chunks, they earn for those chunks and nothing more.

### Step 2.3 — Relay operator payment

Extend the contract to pay relay operators. When a connection was assisted by a relay, the relay logs its involvement. At settlement, the contract pays the relay operator a small fee out of the total, automatically.

The relay operator never sees the content — they only forward encrypted chunks. Their fee is for the connection service, not the data.

### Step 2.4 — VISA / fiat on-ramp

Integrate one payment gateway (start with MoonPay — it has the simplest API). The flow:

1. User pays with VISA card
2. MoonPay receives fiat, converts to crypto
3. Crypto is deposited into the smart contract on the user's behalf
4. User never sees crypto — they just paid a normal card payment

From the user's perspective: enter card number, pay amount, download starts. The blockchain is invisible.

Do not try to integrate all three gateways at once. Get one working perfectly first. Add Transak and Stripe Crypto later.

### Step 2.5 — Seeder wallet and earnings dashboard

Build a simple interface for seeders:

- Shows total chunks delivered, total earned, pending and settled payments
- Lets seeders set a price per gigabyte they are willing to seed at
- Lets seeders choose which files to seed and stop seeding

This does not need to be beautiful. It needs to be functional and trustworthy. A seeder needs to be able to verify they are earning what they expect.

**What you have at the end of Phase 2:** A fully working paid P2P file network. Seeders earn per chunk, relay operators earn per connection, developer earns protocol fee, users pay with card or crypto. Everything automatic.

---

## Phase 3 — The Tiered Resource System

**Goal: the network always has content available, even with zero organic seeders.**

This is the cold start solution. Without it, a new file with one seeder is slow and fragile. With it, any file has guaranteed availability.

### Step 3.1 — Blockchain node registry

Deploy a registry contract where node operators can register as guaranteed seeders. To register, a node must:

- Stake a deposit (say 0.1 ETH) as quality guarantee
- Declare their storage capacity, bandwidth, and geographic region
- Run the Magnetk seeder agent continuously

If a registered node goes offline unexpectedly or serves corrupted data, their stake is slashed proportionally. If they perform well, they earn delivery fees on top of their stake. The stake is returned when they exit the registry.

This creates a pool of always-online, financially committed nodes that the network can rely on when organic seeders are absent.

### Step 3.2 — Automatic tier probing in the SDK

Upgrade the SDK's download logic to probe tiers automatically:

1. Query DHT for organic seeders (Tier 1). If enough peers found, use them.
2. If insufficient, add relay-assisted peers (Tier 2). Charge relay fee.
3. If still insufficient, query the blockchain registry for staked nodes (Tier 3). Charge market rate.
4. For large transfers, always combine all available tiers simultaneously (Tier 4).

The application developer calls `client.download(magnetkLink)`. The tier logic is entirely internal. They see one fast download — they do not see or manage tiers.

### Step 3.3 — Geographic routing

Extend the content router to prefer geographically closer peers at every tier. Use peer IP geolocation (coarse, just country/region level) to rank available sources by estimated latency before assigning chunks.

A user in Nairobi downloads from the nearest staked node in the registry, not from a node in Amsterdam. This matters significantly for large files where latency per chunk multiplies across thousands of chunks.

### Step 3.4 — Predictive pre-seeding contracts

Add a pre-seeding feature to the registry contract. A publisher can:

1. Upload content to their own node and register the magnet link
2. Pay a pre-seeding fee to the contract
3. Contract instructs staked nodes to fetch and cache the content now, before public release
4. At release time, the content is already distributed across dozens of staked nodes globally

For game launches, model releases, and dataset publications with known release dates, this eliminates the cold start entirely. The network is warm before the first user request arrives.

### Step 3.5 — Dead content resurrection bounties

Add a bounty system to the registry contract. When a requester wants content that has zero active seeders:

1. Requester posts a bounty on-chain (e.g. "I will pay 0.05 ETH to anyone who can seed this hash")
2. Any node that can locate, verify, and begin seeding the content claims the bounty
3. Contract verifies the content is now available and releases the bounty automatically

Content that would otherwise be permanently lost can be recovered through economic incentive. No curator, no administrator — the market decides what gets resurrected.

**What you have at the end of Phase 3:** A network with guaranteed availability. Any content, any time, from zero to millions of seeders. The tier system handles everything automatically. This is the point where Magnetk is genuinely better than any CDN for availability.

---

## Phase 4 — Communication Middleware

**Goal: Magnetk is no longer just file transfer. It is a communication platform.**

This phase is about expanding what the network moves. Files are one data type. Messages, events, RPC calls, and streams are others. The transport layer (QUIC) already supports all of them — Phase 4 exposes them as usable APIs.

### Step 4.1 — Real-time messaging

Add a message API to the SDK. Peers can send small payloads (< 1MB) directly to each other addressed by peer ID or topic hash, with sub-50ms delivery over QUIC.

```go
// Send a message to a peer
client.Send(peerID, []byte("hello"))

// Subscribe to a topic
client.Subscribe("topic-hash", func(msg []byte) {
    // handle message
})
```

This is not file transfer. It is messaging — bidirectional, real-time, no chunking overhead for small payloads. The routing uses DHT to find the target peer. QUIC carries the message.

### Step 4.2 — Pub/sub event bus

Extend messaging to a full publish/subscribe system. Peers publish events to a topic hash. Any peer subscribed to that topic receives the event. Topics are addressed by hash — no central registry of topic names.

```go
// Publish an event
client.Publish("topic-hash", eventPayload)

// Subscribe
client.Subscribe("topic-hash", handler)
```

The DHT tracks which peers are subscribed to which topics and routes events to all subscribers. This is a decentralized Kafka — no broker, no central cluster, no operations overhead. It is simply peers telling other peers about events.

### Step 4.3 — Distributed RPC

Add an RPC layer where one peer can call a function on another peer and receive a result. The caller does not need to know the callee's IP address — only the callee's declared capability hash (a hash of the function interface).

```go
// Register a handler
client.Handle("capability-hash", func(input []byte) []byte {
    // process and return result
})

// Call it from another peer
result := client.Call("capability-hash", input)
```

DHT routes the call to a peer that has registered this capability. QUIC carries the request and response. This is a decentralized gRPC — no service registry, no load balancer, no central infrastructure.

### Step 4.4 — Live streaming

Extend the chunk system to support continuous, unbounded streams rather than finite files. A stream is a file with no known end — chunks are produced continuously by the sender and consumed continuously by receivers.

Receivers buffer a small window of chunks and play/process them in order. Late chunks are dropped rather than waited for (QUIC's non-blocking delivery makes this natural). Every receiver simultaneously re-seeds the chunks they have received, so the audience becomes the CDN automatically.

Use cases at this stage: live video, live audio, sensor data streams, real-time log streaming.

### Step 4.5 — State sync with CRDTs

Add a state sync API for distributed data that multiple peers modify concurrently. Under the hood, use Conflict-free Replicated Data Types — mathematical structures that merge concurrent changes automatically without a central authority deciding which version wins.

```go
// Create a shared document
doc := client.NewDoc("doc-hash")

// Make a local change
doc.Set("key", "value")

// Changes sync to all peers holding this doc automatically
// Conflicts resolve automatically via CRDT merge
```

This enables collaborative applications, distributed databases, and offline-first apps where peers sync when they reconnect. No central server decides the canonical state — the CRDT math handles it.

### Step 4.6 — Encrypted P2P channels

Add end-to-end encrypted channels between any two peers. Key exchange happens using elliptic-curve Diffie-Hellman at connection time. Neither the relay nor the blockchain ever sees the plaintext.

Combined with the blockchain's access control (smart contract releases decryption key only to the wallet that paid), this enables zero-knowledge content delivery — not even Magnetk's own infrastructure can read the data in transit.

**What you have at the end of Phase 4:** A complete decentralized communication platform. Files, messages, events, RPC calls, live streams, shared state, encrypted channels. This is the point where Magnetk competes with Kafka, gRPC, WebRTC, and HTTP simultaneously — and beats each of them on at least one important dimension.

---

## Phase 5 — AI Distribution Layer

**Goal: Magnetk becomes the infrastructure for moving AI workloads.**

This phase is where Magnetk becomes genuinely unique. No existing middleware was designed for AI. Magnetk is redesigned around it.

### Step 5.1 — AI model distribution protocol

Build a specific protocol on top of the file transfer layer optimized for model weights:

- Models are chunked by layer, not arbitrary byte ranges — a chunk boundary aligns with a transformer layer boundary
- Each layer chunk has its own hash and can be verified independently
- Peers that have downloaded some layers can immediately begin seeding those layers to others
- The magnet link includes model metadata: architecture, parameter count, precision, version

Publish a reference model (e.g. a small open-source model) on the Magnetk network and benchmark download speed against Hugging Face HTTP download. The speed improvement on popular models should be dramatic.

### Step 5.2 — Differential model sync

When a model is updated (fine-tuned, quantized, or patched), only the changed layers need to be distributed. Implement a delta protocol:

1. Publisher computes diff between old and new model layer by layer
2. Delta chunks are published with a magnet link referencing the base version
3. Nodes that already have the base version download only the delta
4. Delta is applied locally to produce the new version
5. On-chain version history records every version and its delta from the previous

A 40GB model fine-tune that changes 5% of weights becomes a 2GB update instead of a 40GB re-download. This is an order-of-magnitude bandwidth reduction for continuously updated models.

### Step 5.3 — LLM output streaming over QUIC

Implement a streaming inference protocol. When an LLM generates tokens, they are streamed token-by-token over QUIC to the requesting peer. QUIC's 0-RTT and non-blocking streams make this lower latency than HTTP/2 server-sent events, especially on unstable connections.

For edge deployments (in-vehicle assistants, on-device AI, retail kiosks), the latency improvement from QUIC streaming over HTTP is measurable and user-visible. Benchmark and document this comparison.

### Step 5.4 — RAG context delivery

Build a content-addressed document cache for RAG pipelines. When a peer retrieves a document from a vector database to use as LLM context, that document is seeded to the Magnetk network automatically. Other peers querying the same document get it peer-to-peer instead of re-fetching from the vector database.

For enterprise applications where hundreds of users query the same document corpus, this reduces vector database load significantly and speeds up RAG retrieval.

### Step 5.5 — KV cache sharing for shared prefixes

Implement shared KV cache distribution. When an inference node computes the KV cache for a system prompt or a shared document, it content-addresses the cache and seeds it to the network. Other inference nodes that need the same prefix download the pre-computed KV cache instead of recomputing it.

Critical implementation constraint: only seed KV caches for prefixes that contain zero user-specific information — system prompts, retrieved documents, fixed instructional context. Never cache user conversation history. Build the classifier that enforces this boundary before shipping.

### Step 5.6 — Training data pipelines

Build a distributed training data pipeline. A dataset is chunked, content-addressed, and distributed across the peer network. Training nodes fetch chunks in parallel over QUIC — different workers fetch different shards simultaneously. The network serves as a distributed shuffle buffer.

For federated learning across multiple organizations, Magnetk handles data movement while the blockchain handles attribution and compensation per data contribution.

**What you have at the end of Phase 5:** Magnetk is the AI infrastructure layer. Model distribution faster than any CDN, differential updates, low-latency token streaming, shared RAG cache, shared KV cache, distributed training data. Any company running AI at scale has a reason to use this.

---

## Phase 6 — AI Compute Distribution

**Goal: Magnetk coordinates compute across peers, not just data.**

This phase moves from moving AI artifacts to executing AI inference across the network.

### Step 6.1 — Early exit routing

Build a lightweight query classifier that runs at the network edge (on essentially any hardware) and routes incoming inference requests to the appropriate model tier based on estimated complexity:

- Simple factual queries → small distilled model (fast, cheap, low compute)
- Medium complexity → mid-size model
- Complex reasoning, synthesis, multi-step → large model (expensive, but only when necessary)

The classifier itself is tiny — a small embedding model and a simple classification head. It adds single-digit milliseconds of latency but can eliminate 90%+ of traffic to expensive large models for applications where most queries are simple.

This is the easiest compute reduction to implement and has the largest impact on cost.

### Step 6.2 — Model sharding across peers

Implement pipeline parallelism across multiple peers. A large model is split by layers — each peer holds a contiguous range of layers. Input activations pass through peers sequentially over QUIC.

Start with a fixed sharding configuration (e.g. a 70B model split across 4 peers, ~17B parameters each). Each peer must declare how many layers they can hold when registering in the blockchain node registry. The shard orchestrator (part of the SDK) assigns layers to peers and coordinates the pipeline.

Measure end-to-end latency for a 70B model sharded across 4 consumer GPUs versus a single A100. The shard version will have higher per-token latency due to network overhead, but makes the model accessible to hardware that could not run it at all.

### Step 6.3 — Speculative decoding across peers

Separate the draft and verifier roles. A lightweight peer (modest hardware) runs a small draft model and generates candidate tokens. A powerful peer runs the large verifier model and accepts or rejects the draft tokens in parallel.

The verifier does less sequential work than running generation itself. The draft peer does work it is well-suited for. Net effect: throughput improvement compared to the verifier running alone, with compute costs split across two tiers of hardware.

Payment: the draft peer earns per accepted token (tokens the verifier accepted). Rejected tokens are not paid — this incentivizes the draft model to be accurate.

### Step 6.4 — Federated inference

For regulated industries where data cannot leave the local environment, implement split computation inference. The input is split or encrypted before leaving the client. Multiple compute peers each process a portion without seeing the full input. Results are assembled at the client.

This is technically complex — homomorphic encryption at inference speed is still an active research area. Start with a simpler approach: split the model across trusted peers within an organization's own infrastructure (same cloud account, same VPC) where "federated" means across departments rather than across untrusted public peers. True multi-party untrusted federated inference comes later.

### Step 6.5 — Verifiable compute and proof of inference

Implement zero-knowledge proofs that a specific model was run on specific input and produced specific output, without revealing input, output, or model weights to any third party.

This is the most technically difficult item in the entire roadmap. Use an existing ZK proving system (RISC Zero or SP1) rather than implementing from scratch. The Magnetk contribution is the integration layer: publishing proofs on-chain, verifying them in the payment contract, and releasing payment only after proof verification.

At this stage, proofs can only be generated for small models — ZK proof generation for large models is not yet practical in real time. Build the infrastructure now; as ZK proving hardware improves, larger models become provable automatically.

**What you have at the end of Phase 6:** Magnetk handles not just data movement but AI compute coordination. Inference requests are routed by complexity, large models run across peer pipelines, privacy-sensitive inference runs in federated configurations, and every computation can optionally be proven on-chain.

---

## Phase 7 — AI Agent Mesh

**Goal: AI agents use Magnetk as their native communication and economy layer.**

### Step 7.1 — Agent discovery via DHT

Agents register their capabilities on the DHT using a capability hash derived from a structured description of what they do (a schema: input type, output type, task category). Other agents query the DHT to find agents that can handle a given capability.

```go
// Agent registers itself
agent.Register(Capability{
    InputType:  "text/document",
    OutputType: "text/summary",
    Task:       "summarization",
})

// Another agent finds it
peers := client.FindCapability("summarization")
```

No central agent registry. No orchestration server. Agents find each other the same way peers find files — through the DHT.

### Step 7.2 — Agent-to-agent messaging and payment

Agents send tasks to each other using the real-time messaging layer from Phase 4. Payment for completed tasks uses the smart contract payment layer from Phase 2.

The flow: Agent A sends a task and a payment to Agent B's wallet address. Agent B completes the task and sends the result back. The smart contract releases payment upon result delivery. If Agent B fails to deliver, payment is refunded automatically after timeout.

This is a decentralized AI task market. Agents are buyers and sellers of compute. No central marketplace takes a cut beyond the protocol fee.

### Step 7.3 — Shared context and memory

Agents working on related tasks share context using the content-addressed document cache from Phase 5. An agent that retrieves a document seeds it to the network — another agent working on the same task gets it peer-to-peer. An agent that produces an intermediate result content-addresses it and announces it via pub/sub — other agents can retrieve it without re-requesting it from the first agent.

This is the foundation of multi-agent collaboration on shared problems without a central coordinator holding state.

### Step 7.4 — Semantic response caching for agents

For agent pipelines where the same sub-task is called repeatedly with the same or very similar input, implement semantic caching at the agent layer. An agent caches its output keyed by the semantic embedding of its input. On a cache hit, the result is served directly without inference.

Apply the same strict constraints as described in MagnetkCore: only cache outputs for stateless, context-free sub-tasks. Any task with session state, user identity, or external dependencies bypasses the cache entirely.

**What you have at the end of Phase 7:** A fully decentralized AI agent economy. Agents discover each other, communicate directly, share context, and pay each other — all without a central orchestrator. This is the infrastructure layer for the next generation of AI applications.

---

## Phase 8 — Platform Expansion

**Goal: make Magnetk accessible everywhere and to everyone.**

### Step 8.1 — Mobile SDKs (React Native / Flutter)

Port the SDK to mobile. Mobile peers can seed files they have downloaded (over WiFi only, with user consent and bandwidth limits). Mobile peers can participate in the agent mesh as lightweight clients. Payment works through the same VISA on-ramp.

Mobile seeding is opt-in and battery/bandwidth aware — it stops automatically when the device goes to cellular or battery drops below a threshold.

### Step 8.2 — Browser SDK via WebRTC

Build a pure-browser SDK using WebRTC for peer connections. A user opens a webpage and immediately participates in the Magnetk network — no installation, no Node.js. They can download files, seed files, and send messages. They cannot run inference (GPU access from browser is too limited) but they can be a full participant in the data network.

This is the zero-friction onboarding path. The first time a non-technical user uses Magnetk is when a game studio uses it for distribution and the user just opens a browser to download.

### Step 8.3 — IoT lightweight client

Build a minimal C implementation (not C++) of the Magnetk peer protocol small enough to run on embedded hardware (ESP32, Raspberry Pi Zero class devices). IoT devices join the network as data producers — streaming sensor readings, telemetry, and device state directly to subscribing peers with micropayment per reading.

### Step 8.4 — Enterprise QoS contracts

Build the enterprise tier of the payment system. Large customers (game studios, AI companies, enterprise software vendors) pre-purchase delivery guarantees:

- Minimum seeder count for their content at all times
- Maximum delivery latency SLA
- Geographic availability guarantees by region
- Pre-seeding for scheduled releases

These are on-chain contracts with staked blockchain nodes. The enterprise pays upfront, the nodes commit bandwidth, the contract enforces SLAs automatically and slashes stake for violations.

---

## Phase 9 — Network Maturity

**Goal: the network runs itself with no dependency on any person, company, or server.**

### Step 9.1 — Protocol governance on-chain

Move protocol parameter decisions (fee percentages, stake requirements, tier thresholds) to on-chain governance. Token holders vote on protocol changes. No single entity — including the developer — can unilaterally change how the network works.

This is what makes Magnetk genuinely decentralized rather than just technically distributed. A company can shut down a server. Nobody can shut down a smart contract that governance has taken ownership of.

### Step 9.2 — Reputation system for all participants

Build an on-chain reputation score for every network participant — seeders, relay operators, staked nodes, compute providers, agents. Reputation is computed from:

- Delivery success rate (chunks delivered vs promised)
- Uptime (for staked nodes)
- Latency (speed of delivery)
- Stake history (no slashing = good reputation)

The content router prefers high-reputation peers at every tier. Bad actors are economically excluded — they earn less, eventually cannot attract requests, and either improve or leave.

### Step 9.3 — Cross-chain payment support

Support payment in multiple blockchain ecosystems — not just Ethereum. Add support for at least one high-throughput low-fee chain (Solana, Arbitrum, or Base) for micropayments where Ethereum gas fees would make small transfers uneconomical.

A seeder earning $0.001 per chunk delivered should not pay $0.01 in gas fees per settlement. Layer 2 rollups or alternative chains solve this — implement batched settlement where many small payments are aggregated and settled in one on-chain transaction.

### Step 9.4 — Network health monitoring (decentralized)

Build a decentralized network health system. Any peer can observe and report on network conditions — average latency by region, seeder availability by content hash, tier usage statistics. These observations are aggregated off-chain and the summary published on-chain periodically.

No central monitoring server. The network monitors itself through the observations of its participants.

---

## Summary Timeline

| Phase | Name | Key Outcome |
|---|---|---|
| 1 | Solid Foundation | Reliable multi-seeder P2P with DHT |
| 2 | Payment Layer | Seeders earn, downloaders pay, developer earns |
| 3 | Tiered Resources | Guaranteed availability, cold start solved |
| 4 | Communication Middleware | Messages, events, RPC, streams, state sync |
| 5 | AI Distribution | Models, deltas, streaming, RAG, KV cache |
| 6 | AI Compute | Routing, sharding, federated, verifiable |
| 7 | Agent Mesh | Decentralized AI agent economy |
| 8 | Platform Expansion | Mobile, browser, IoT, enterprise |
| 9 | Network Maturity | Governance, reputation, cross-chain |

**Start Phase 1 now. Do not think about Phase 7 until Phase 3 works.**

Each phase has a clear, testable outcome. You know Phase 1 is done when multi-seeder download works reliably and DHT finds peers without the relay. You know Phase 2 is done when a seeder earns ETH automatically for delivering chunks with no manual steps. You know Phase 3 is done when a file with zero organic seeders is still available through staked nodes.

Build sequentially. Test each phase before starting the next. The architecture compounds — a bug in Phase 1 creates ten bugs in Phase 5.

---

*step.md — Magnetk Development Roadmap*
*Version 1.0 — June 2026*
*To be read alongside MagnetkCore.md*