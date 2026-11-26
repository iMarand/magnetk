# Magnetk v2 architecture

This document describes what is implemented in this repository today: the
transport, the relay, the node engine, the wire protocol and the SDKs built
on top of them. The long-term vision (payments, DHT, AI mesh) is in
[MAGNETK_CORE.md](MAGNETK_CORE.md) and [ROADMAP.md](ROADMAP.md).

```
 Your app ─ Go SDK │ Python SDK │ C++ SDK │ C ABI │ magnetk CLI │ Android app
                   └──────┬──────────────────────────────────────┘
                          ▼
              node engine (go/pkg/node)  ── one per process / per Node object
   messaging · pub/sub · request/reply · pipes · files · presence · failover
                          │
              netx (go/pkg/netx): one Conn/Stream API over two transports
         ┌────────────────┴────────────────┐
   QUIC (MsQuic 2.x, C transport)     TCP + TLS 1.3 + yamux (pure Go)
   UDP :4002, datagrams, migration    TCP :4002, works where UDP is blocked
                          │
                ┌─────────┴─────────┐
              relay A  ◄── link ──►  relay B      (federation, go/pkg/relay)
                ▲   ▲                  ▲
             node  node ◄── direct ──► node       (peer-to-peer when reachable)
```

## Topology: relays first, peer-to-peer when possible

Every node keeps **one connection to one relay** (a cheap VPS is enough).
Through it the node gets messaging, presence, lookups, the file registry
and, when peers cannot reach each other, relayed data pipes. This works
behind any NAT or firewall that allows outbound traffic, including mobile
networks.

When two nodes *can* reach each other (same LAN, public IP, port forward,
UPnP) pipes and file transfers go **directly** between them and the relay is
only used for signalling. `Route` selects the policy: `auto` (try direct for
up to 2 s, then relay), `direct`, or `relay`.

Why relays instead of pure P2P discovery: a relay gives a single round trip
for every message (no DHT walk), works on every network, and lets messages
be routed, fanned out, load-balanced and stored for offline nodes. One relay
on a 1 vCPU VPS handles tens of thousands of nodes; two relays in different
regions give redundancy and lower latency (see [RELAY_DEPLOYMENT.md](RELAY_DEPLOYMENT.md)).

### Federation

Relays started with `-peers` and the same `-secret` link to each other (one
link per pair; the relay with the smaller id keeps its outbound link if both
dial). Over a link they exchange:

* `RouteAdd/RouteDel` — which nodes are attached where, so a message to a
  node on the other relay is forwarded in one hop;
* `InterestAdd/InterestDel` — topic patterns with subscribers, so
  publications cross a link only when someone on the other side wants them;
* `FwdPub` — forwarded publications, carrying the queue groups already
  served so each queue group still gets exactly one copy;
* file registry entries and relayed pipes.

Nodes list several relays (`RELAYS=a:4002,b:4002`). They connect to the
first that answers — the next relay is tried in parallel after 400 ms, so an
unreachable entry (e.g. a LAN relay while a phone is on mobile data) costs
under half a second — and fail over automatically. A 5 s heartbeat detects
a dead relay within 15 s; `Node.Reconnect()` (called by the Android app on
every network change) reconnects immediately.

## Transport

### QUIC (default)

`c/src/transport.c` wraps MsQuic with a small non-blocking API used by Go
through cgo (`go/pkg/transport`, which vendors a generated copy of the C
sources — `go generate` keeps it in sync, `TestCSourcesInSync` checks it).

* **Readiness callbacks instead of blocking threads**: MsQuic events copy
  data into a per-stream ring buffer and signal Go; goroutines park on
  channels, so thousands of streams cost no OS threads.
* **Receive ring with backpressure**: when a stream's ring is full the
  receive is partially accepted, MsQuic stops delivering and flow control
  slows the sender; reading below half re-enables it.
* **Bounded send window** per stream: `max(SEND_WINDOW, MsQuic's ideal
  send buffer)` capped at 64 MiB, so a fast writer cannot exhaust memory.
* **Datagrams** (RFC 9221) for unreliable, lowest-latency messages.
* **Congestion control**: Cubic by default. BBR is available
  (`CONGESTION_CONTROL=bbr`) but it is an MsQuic preview feature and in our
  tests its pacing collapsed after a burst on an idle connection (transfers
  stuck at 2–5 MB/s with zero loss), so it is opt-in.
* Execution profiles (`max_throughput`, `low_latency`, `realtime`,
  `scavenger`) map to MsQuic's profiles.

### TCP + TLS 1.3 + yamux (fallback)

Pure Go, used when UDP is blocked, when MsQuic is not available (builds with
`CGO_ENABLED=0` or `-tags nomsquic`, e.g. the Android app today) or when
forced with `TRANSPORT=tcp`. The relay serves both on the same port number.
On TCP the first byte is sniffed: a TLS ClientHello starts a v2 session,
anything else goes to the legacy v1 handler (JavaScript SDK compatibility).

In `auto` mode the dialer races both, happy-eyeballs style: QUIC first, TCP
joins after 400 ms, first to finish wins.

### Security

Every connection is TLS 1.3 (QUIC always is). Relays generate a self-signed
certificate unless `TLS_CERT_FILE`/`TLS_KEY_FILE` (or a Windows store
thumbprint) are given; clients verify certificates when
`NO_CERT_VALIDATION=false`. Relays can require tokens (`AUTH_TOKENS`),
federation links require the cluster secret, direct pipes carry a
per-pipe token, and every file chunk is verified against its SHA-256.

## Node engine

`go/pkg/node` — one implementation used by every SDK:

| Feature | API (Go names) | Notes |
|---|---|---|
| Direct messages | `Send(to, data, opts…)`, `Receive`, `Messages()` | `WithReceipt` (delivery ack), `WithStore` (offline queue), `Unreliable` (QUIC datagram) |
| Pub/sub | `Subscribe(pattern, handler)`, `Publish` | wildcards `a.*` (one token) and `a.>` (rest); ordered per subscription |
| Queue groups | `QueueSubscribe(pattern, queue, h)` | each message to one member — load balancing |
| Request/reply | `Request(node)`, `RequestTopic(topic)`, `HandleRequests`, `OnRequest` | topic requests are load-balanced over all handlers |
| Presence & lookup | `WatchPresence`, `Lookup`, `ListNodes` | join/leave events, direct addresses, metadata |
| Pipes | `OpenPipe(to, service)`, `HandlePipe`, `AcceptPipe` | reliable byte streams, `io.ReadWriteCloser` + `CloseWrite` + deadlines |
| Files | `Share`, `Download`, `FindProviders`, `Unshare` | multi-source, pipelined, verified, resumable |
| Health | `Ping`, `Info`, `Stats`, `Security`, `RelayStats`, `Reconnect` | |

**Flow control end to end.** Outgoing frames go through a byte-bounded
queue (`SendQueueBytes`, default 16 MiB): `Send`/`Publish` block when it is
full instead of growing memory. Incoming messages go to bounded buffers
(inbox 8192, 1024 per subscription); when one is full the reader waits up
to `SLOW_CONSUMER_WAIT` (250 ms) — absorbing bursts without loss — and only
a consumer that stays stuck has messages dropped (counted in
`Subscription.Dropped()` and `Stats().Dropped`). The relay applies the same
idea per node (`MAX_QUEUE_PER_NODE`, `SLOW_CONSUMER=drop|disconnect`).

**Transfer tuning.** The pipeline depth per stream adapts to the measured
throughput (AIMD every 8 chunks, 2 to 32 requests in flight). Chunks of
compressible files are sent deflated as `CHUNK_DATA_Z` (0xA5) when the
downloader sets `ChunkAcceptCompressed` in `CHUNK_REQ`; older peers ignore
the flag. Byte-range downloads fetch only the chunks covering the range.

**Files.** `Share` hashes the file once (parallel, cached by size+mtime)
into a manifest: whole-file SHA-256, chunk size, per-chunk SHA-256 and a
Merkle-style root. The link
`magnetk:?xt=urn:sha256:<hash>&dn=<name>&xl=<size>&cs=<chunk>&mr=<root>&relay=<a>&relay=<b>`
is self-contained. `Download` asks the relay for providers, opens
`StreamsPerSource` pipes to each (direct first), keeps several chunk
requests in flight per stream, verifies every chunk, writes to
`<file>.mkpart` with a resume bitmap (`.mkpart.state`) and checks the
whole-file hash at the end. Downloaders can keep seeding (`Seed: true`).

## Relay

`go/pkg/relay` (`magnetk relay`): sessions, routing, subscriptions (exact
map + wildcard list), queue groups, request routing, presence, lookups, the
file registry, the offline store (bounded by count, bytes and TTL), pipe
splicing (pooled 256 KiB buffers, half-close propagation), federation links,
legacy v1 support (`LEGACY_PORT`, default 4003) and statistics
(`-http :8080` → `/stats`, `/metrics` for Prometheus, `/healthz`).

A reconnecting node replaces its old session atomically: watchers do not see
a spurious `leave`, and the old session's cleanup cannot remove the new
session's files or routes.

## Wire protocol v2

Every frame: `u32 length (big endian) | u8 type | body`. The 5-byte header
is the same as v1. Bodies are big-endian integers, `u16`-length strings and
a trailing raw payload (no JSON, no base64). Types:

| Range | Frames |
|---|---|
| 0x50–0x55 | `HELLO`, `WELCOME`, `ERROR`, `PING`, `PONG`, `BYE` |
| 0x60–0x6E | `SEND`, `DELIVER`, `ACK`, `PUB`, `SUB`, `UNSUB`, `REQ`, `REPLY`, `PRESENCE`, `WATCH`, `LOOKUP`, `LOOKUP_RES`, `STATS_REQ`, `STATS_RES` |
| 0x70–0x73 | `FILE_PUBLISH`, `FILE_UNPUBLISH`, `FILE_FIND`, `FILE_PROVIDERS` |
| 0x80–0x83 | `PIPE_OPEN`, `PIPE_INCOMING`, `PIPE_ACCEPT`, `PIPE_REJECT` |
| 0x90–0x94 | federation: `ROUTE_ADD`, `ROUTE_DEL`, `INTEREST_ADD`, `INTEREST_DEL`, `FWD_PUB` |
| 0xA0–0xA4 | file transfer inside an `mk.file` pipe: `MANIFEST_REQ/RES`, `CHUNK_REQ`, `CHUNK_DATA`, `FILE_ERROR` |

A session starts with `HELLO {version, node id, token, flags, meta, client,
direct addresses}` on the first stream; the relay answers `WELCOME {node id,
relay id, observed address, max frame, server time}` or `ERROR`. That stream
then carries all control traffic. Each pipe is a separate stream that starts
with `PIPE_OPEN` (or `PIPE_INCOMING` from the relay) and then carries raw
bytes. Datagrams carry `SEND`/`DELIVER` frames.

Flags on messages: `ACK` (send a delivery report), `STORE` (queue for an
offline target), `DATAGRAM` (arrived unreliably), `FORWARDED` (crossed a
relay link). Encoders/decoders: `go/pkg/wire/messages.go`.

## SDKs

| SDK | How it works | Location |
|---|---|---|
| Go | the engine itself, plus a facade package | `sdk/go` (`github.com/iMarand/magnetk/sdk/go/magnetk`) |
| C ABI | `libmagnetk` built from `go/capi` (c-shared): opaque handles, error codes, JSON options | `sdk/c/include/magnetk.h` |
| Python | ctypes over libmagnetk, no compiler needed | `sdk/python` |
| C++ | header-only C++17 wrapper that loads libmagnetk at run time | `sdk/cpp/include/magnetk.hpp` |
| JavaScript | v1 protocol, served by the relay's legacy port | `sdk/js` |
| Android | field-test app; the engine as a JNI library | `android/`, `go/mobile` |

## Field testing

`magnetk diag host` (PC) + the Android app or `magnetk diag client`
measure latency, throughput, integrity, security and reconnect behaviour on
real networks and send every log line to the PC. See
[FIELD_TEST.md](FIELD_TEST.md).

## Identity and secure chat

* `pkg/identity`: the Ed25519 identity key (with an X25519 encryption key
  derived from the same seed), fingerprints, safety numbers, per-message
  sealing (X25519 ephemeral-static + static-static ECDH, HKDF-SHA256,
  AES-256-GCM), and the contacts store (pinned keys, allowed, blocked,
  verified).
* HELLO carries an optional public key, timestamp and signature (appended
  fields, compatible with older peers). The relay verifies them and binds the
  node id to the key (`IDENTITIES_FILE`).
* `pkg/chat`: chat messages are direct messages tagged `C`: a JSON header
  plus the attachment. Sealed messages put everything except id, from, to,
  time, size and keys inside the ciphertext. Keys are exchanged through a
  direct request answered inside the engine
  (`Node.HandleSystemRequests`), so apps do not have to read it.
* Attachments over 256 KB (and `SendFile`) go through the file engine: the
  file is encrypted with a fresh key (AES-256-GCM in 1 MiB segments,
  deflated first if compressible) and shared, and the sealed message carries
  only the link, key and SHA-256. The sender stops sharing once the receiver
  acknowledges.
* Exposed through the C ABI (`mk_identity`, `mk_chat_send`,
  `mk_chat_contact`, `MK_KIND_CHAT`), the Python, C++ and Go SDKs, the CLI
  (`magnetk chat`, `magnetk id`) and the Android app.

Details and limits: [SECURITY.md](SECURITY.md).

