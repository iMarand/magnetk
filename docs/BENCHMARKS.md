# Benchmarks

Reproduce with `magnetk bench -local all` (embedded relay + two nodes in one
process, loopback) and, for real networks, the field-test kit
([FIELD_TEST.md](FIELD_TEST.md)). Numbers below were measured on a 4-core
Windows 11 laptop; relay, both nodes and the benchmark share those 4 cores,
so they are a floor, not a ceiling. Ranges are from repeated runs.

## Loopback, one machine

| Benchmark | QUIC (MsQuic, Cubic + HyStart++) | TCP + TLS 1.3 + yamux |
|---|---|---|
| pub/sub, 128 B messages, delivered | 190k – 235k msg/s, 100 % delivered | ~183k msg/s, 100 % |
| request/reply round trip (a → relay → b → relay → a) | p50 0.6 – 1.1 ms, p99 ~3.5 ms | p50 ~0.55 ms, p99 ~2.2 ms |
| request/reply throughput, 16 concurrent | ~11.8k req/s | ~16.5k req/s |
| pipe through the relay | 38 – 67 MB/s | ~240 MB/s |
| pipe peer-to-peer | 100 – 124 MB/s | ~173 MB/s |
| file download through the relay (verified) | 38 – 64 MB/s | ~154 MB/s |
| file download peer-to-peer (verified) | ~103 MB/s | ~169 MB/s |

Why TCP wins on loopback: MsQuic's own loopback ceiling on this machine
(`secnetperf`, no Magnetk involved) is about 550 Mbit/s ≈ 69 MB/s per
connection, while kernel TCP reaches 1.6 Gbit/s. A relayed pipe crosses
two QUIC connections in the same process, hence roughly half the ceiling.
On real networks the link, not the CPU, is the limit, and QUIC brings what
loopback cannot show: no head-of-line blocking between streams (a big
transfer does not delay messages on the same connection), datagrams and
connection migration (NAT rebinding). `TRANSPORT=auto` races
both and uses whichever connects first.

## Before / after this rework

| | v1 | v2 |
|---|---|---|
| 200 MB direct transfer | 55 MB/s (5.35 s) | 108 – 124 MB/s QUIC, ~170 MB/s TCP |
| Relay data path | placeholder (no relayed transfers) | 38 – 67 MB/s QUIC, ~240 MB/s TCP |
| Binary size | 55 MB | 9 MB |
| Messaging, pub/sub, RPC | not implemented | numbers above |

## Bugs the benchmarks and field tests found (fixed)

| Symptom | Cause | Fix |
|---|---|---|
| Relayed transfers fell to 2–5 MB/s after the first download, zero packet loss | MsQuic's BBR (preview) pacing collapsed on an idle connection | Cubic is the default, BBR opt-in |
| Cubic: request p99 6.9 ms / max 245 ms during bulk traffic, pub/sub 96k/s | slow-start overshoot → queueing delay | HyStart++ enabled: p99 3.5 ms / max 14 ms, pub/sub 235k/s |
| 5 % of a 5 000-message burst dropped with a trivial handler | subscriber buffer overflow dropped instantly | bounded wait (`SLOW_CONSUMER_WAIT`) before dropping |
| Phone reconnect caused a stale "leave" and could drop the new session's files/routes | old session cleanup raced the replacement | atomic session replacement |
| Unreachable first relay delayed connecting by 10–15 s | relays dialed one after another | staggered parallel dial (400 ms) |
| Android: node could not start | certificate written to an unwritable temp dir | QUIC-less builds skip it; app sets its own temp dir |
| Sub-millisecond latencies reported as 0 on Windows | `time.Now()` resolution ~0.5 ms | `pkg/hrtime` (QueryPerformanceCounter) |

## Android (emulator, Android 14, x86_64)

All 13 field tests pass. Throughput is 2–3 MB/s because the emulator's
user-mode network is slow; real devices are expected to be limited by
Wi-Fi / mobile throughput instead. Network switch measured by the app:
Wi-Fi → cellular reconnected in **163 ms**, cellular → Wi-Fi in **81 ms**,
no monitor pings lost. Real-device numbers: run the app and see
`diag-data/logs/`.
