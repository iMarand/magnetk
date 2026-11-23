# Magnetk Go SDK

`github.com/iMarand/magnetk/sdk/go/magnetk` is the Go entry point to
Magnetk: messaging, pub/sub, request/reply, byte pipes, verified
multi-source file transfer and embeddable relays. It is a thin, stable
facade over the engine in `github.com/iMarand/magnetk/go` (the same engine
behind the Python, C, C++ SDKs and the CLI); every type is an alias of the
engine type, so you can drop down to `pkg/node`, `pkg/relay` or
`pkg/files` at any time.

## Install

```bash
go get github.com/iMarand/magnetk/sdk/go
```

(`go get` works once the `go` and `sdk/go` modules are tagged; until then
add `replace github.com/iMarand/magnetk/go => <path to>/magnetk/go` to
your `go.mod`. Inside this repository `go.work` already does that.) QUIC needs cgo and MsQuic (`scripts/build.*` builds
it; at run time `msquic.dll` / `libmsquic.so` must be next to the binary or
on the library path). Without cgo (`CGO_ENABLED=0`) or with `-tags
nomsquic` everything works over TCP + TLS 1.3 — `magnetk.QUICAvailable()`
tells which you got.

## Quick start

```go
ctx := context.Background()
n, err := magnetk.Connect(ctx, "vps1.example.com:4002", "vps2.example.com:4002")
if err != nil {
    log.Fatal(err)
}
defer n.Close()

n.Subscribe(ctx, "chat.>", func(m *magnetk.Message) {
    fmt.Printf("%s on %s: %s\n", m.From, m.Subject, m.Data)
})
n.Publish(ctx, "chat.general", []byte("hello"))
```

`Connect` creates the node, connects to the first relay that answers
(failing over to the others, reconnecting forever) and returns once
connected. Use `ConnectConfig(ctx, magnetk.Config{...})` for options, or
`New` + `Start` for full control.

## Configuration (`magnetk.Config`)

| Field | Default | |
|---|---|---|
| `Relays` | — | `"host:port"` list, preference order |
| `ID`, `Token`, `Meta` | relay-assigned, none, "" | identity, relay auth, metadata seen by others |
| `Transport` | `ModeAuto` | `ModeAuto` (QUIC, TCP fallback), `ModeQUIC`, `ModeTCP` |
| `QUIC` | | `QUICConfig{Profile, Congestion, SendWindow, RecvWindow, KeepAlive, ...}` |
| `Listen`, `ListenAddr`, `AdvertiseAddrs`, `UPnP` | off | accept peer-to-peer connections |
| `Route` | `RouteAuto` | pipes/files: `RouteAuto`, `RouteDirect`, `RouteRelay` |
| `WatchPresence` | false | join/leave events in `Messages()` |
| `InboxSize`, `SubBuffer`, `SlowConsumerWait` | 8192, 1024, 250 ms | incoming buffers, burst tolerance |
| `SendQueueBytes` | 16 MiB | outbound queue (Send/Publish block when full) |
| `RequestTimeout` | 10 s | default for requests and receipts |
| `HeartbeatInterval`, `HeartbeatTimeout` | 5 s, 15 s | dead-relay detection |
| `ReconnectMin`, `ReconnectMax` | 250 ms, 10 s | reconnect backoff |
| `ChunkSize`, `StreamsPerSource`, `CacheDir`, `LinkRelays` | auto, 4, user cache, Relays | files |
| `VerifyPeer`, `CAFile`, `CertFile`, `KeyFile` | off | TLS |
| `Logger`, `OnEvent` | silent | engine log, connection events |

Load the same settings file the CLI uses:
`cfg, _ := magnetk.NodeConfigFromSettings("config/settings.conf")`
(environment variables `MAGNETK_<KEY>` override it).

## Messaging

```go
n.Send(ctx, "bob", data)                                // fire and forget
n.Send(ctx, "bob", data, magnetk.WithReceipt())         // wait for delivery (ErrNoRoute if offline)
n.Send(ctx, "bob", data, magnetk.WithStore())           // keep for an offline node
n.Send(ctx, "bob", data, magnetk.Unreliable())          // QUIC datagram, lowest latency
n.Publish(ctx, "sensors.room1.temp", data)

sub, _ := n.Subscribe(ctx, "sensors.*.temp", handler)          // ordered, own goroutine
sub, _ = n.QueueSubscribe(ctx, "jobs.>", "workers", handler)   // load-balanced group
sub.Unsubscribe(); sub.Dropped()

reply, err := n.Request(ctx, "bob", data)               // to a node (OnRequest handler)
reply, err = n.RequestTopic(ctx, "svc.resize", data)    // to any server of a topic
n.HandleRequests(ctx, "svc.resize", func(m *magnetk.Message) ([]byte, error) { ... })
n.OnRequest(func(m *magnetk.Message) ([]byte, error) { ... })

m, err := n.Receive(ctx)            // direct messages, presence, handler-less subscriptions
for m := range n.Messages() { ... }
```

Patterns: `a.b` exact, `a.*` one token, `a.>` one or more tokens. A handler
error becomes `*magnetk.RemoteError` at the requester.

## Pipes

```go
n.HandlePipe("upload", func(p *magnetk.Pipe) {
    defer p.Close()
    io.Copy(dst, p)
})
p, err := n.OpenPipe(ctx, "bob", "upload", magnetk.WithRoute(magnetk.RouteAuto), magnetk.WithMeta("photo.jpg"))
io.Copy(p, src); p.CloseWrite()
```

`*Pipe` is an `io.ReadWriteCloser` with `CloseWrite`, deadlines, and
`Peer`, `Service`, `Meta`, `Direct` fields. `RouteAuto` tries a direct
connection for up to 2 s (the peer must have `Listen: true`) and falls back
to the relay.

## Files

```go
f, _ := n.Share(ctx, "big.iso", nil)           // f.Link, f.Hash, f.Size
res, err := n.Download(ctx, link, "downloads/", &magnetk.DownloadOptions{
    Progress: func(p magnetk.Progress) { fmt.Printf("%.1f%% %.1f MB/s\n", p.Percent(), p.BytesPerSec/1e6) },
    Seed:     true, // keep sharing afterwards
})
prov, _ := n.FindProviders(ctx, hash)
n.Unshare(ctx, hash)
l, _ := magnetk.ParseLink(link)                // FileHash, FileName, FileSize, Relays...
```

Downloads use all providers in parallel, verify every chunk and the whole
file, and resume from `<file>.mkpart`.

## Relays

```go
r, err := magnetk.StartRelay(magnetk.RelayConfig{
    Listen:        "0.0.0.0:4002",
    AuthTokens:    []string{"secret"},
    Peers:         []string{"other-vps:4002"},   // federation
    ClusterSecret: "cluster-secret",
})
defer r.Close()
fmt.Println(r.Addr(), r.Stats().Nodes)
```

## Health and network changes

`n.Info()` (relay, protocol, observed address, RTT), `n.Ping(ctx)`,
`n.Stats()`, `n.Security()` (TLS version, cipher, certificate
fingerprint), `n.RelayStats(ctx)`, `n.ListNodes(ctx, prefix)`,
`n.Lookup(ctx, id)`. Call `n.Reconnect()` when the device's network
changes (Wi-Fi ↔ mobile data) to reconnect immediately instead of waiting
for the heartbeat.

## Errors

`errors.Is(err, magnetk.ErrNoRoute)` etc.: `ErrClosed`, `ErrNotConnected`,
`ErrDisconnected`, `ErrUnauthorized`, `ErrNoRoute`, `ErrDropped`,
`ErrTimeout`, `ErrNoProviders`, `ErrNotRequest`, `ErrTooLarge`; typed
`*RemoteError`, `*PipeRejectedError`.

## Examples

`examples/` — run with `go run ./examples/<name>`:

| Example | |
|---|---|
| `chat` | rooms over pub/sub with presence |
| `pubsub` | throughput with a queue group |
| `rpc` | load-balanced request/reply service |
| `pipe` | byte stream throughput, relay vs direct |
| `share`, `download` | file sharing |
| `relay` | embedded relay, federation |

Tests: `go test ./...` in `sdk/go`.
