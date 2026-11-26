# Magnetk C++ SDK

A header-only C++17 SDK (`include/magnetk.hpp`) for messaging, pub/sub,
request/reply, byte pipes and verified multi-source file transfer. It wraps
the C ABI of `libmagnetk` (the same engine as the Go and Python SDKs and
the `magnetk` CLI) and **loads it at run time**, so you link nothing except
the platform loader and threads.

## Build

```bash
scripts/build.sh                      # or scripts\build.bat: builds build/lib/libmagnetk.*
cmake -S sdk/cpp -B sdk/cpp/build
cmake --build sdk/cpp/build --config Release
sdk/cpp/build/bin/test_sdk            # 19 integration checks
```

Works with MSVC 2019+, MinGW-w64 GCC and Clang/GCC on Linux and macOS.
CMake copies `libmagnetk` and `msquic` from `MAGNETK_LIB_DIR` (default
`<repo>/build/lib`) next to the executables in `build/bin` (plus the MinGW
runtime DLLs). At run time the library is found via `MAGNETK_LIB` (full
path), the executable's directory, then the system search path.

In your own CMake project:

```cmake
add_subdirectory(path/to/magnetk/sdk/cpp)
target_link_libraries(myapp PRIVATE magnetk)   # include dirs + dl/threads
```

## Quick start

```cpp
#include <magnetk.hpp>
#include <iostream>

int main() {
    magnetk::Relay relay;                       // embedded, 127.0.0.1:<random>
    magnetk::NodeOptions o;
    o.relays = {relay.address()};               // or {"vps1:4002", "vps2:4002"}

    o.id = "bob";   magnetk::Node bob(o);
    o.id = "alice"; magnetk::Node alice(o);

    bob.subscribe("chat.>");
    bob.serve("svc.upper");
    bob.on_message([](const magnetk::Message& m) -> std::optional<std::string> {
        if (m.is_request()) { std::string s = m.data; for (auto& c : s) c = toupper(c); return s; }
        std::cout << m.from << " on " << m.subject << ": " << m.data << "\n";
        return std::nullopt;
    });

    alice.publish("chat.general", "hello");
    std::cout << alice.request_topic("svc.upper", "loud") << "\n";   // LOUD
}
```

## API

### `NodeOptions`

`relays`, `id`, `token`, `meta`, `transport` (`auto|quic|tcp`), `route`
(`auto|direct|relay`), `listen` (accept peer-to-peer), `listen_addr`,
`advertise`, `upnp`, `watch_presence`, `verify_peer`,
`request_timeout_ms`, `connect_timeout_ms`, `inbox_size`, `sub_buffer`,
`slow_consumer_wait_ms`, `chunk_size`, `streams_per_source`, `cache_dir`,
`link_relays`, `congestion` (`cubic` default, `bbr`), `log`.

### `Node`

| Call | Description |
|---|---|
| `Node(opts)` | connects (throws on failure); fails over between relays and reconnects automatically |
| `id()`, `info_json()`, `ping_us()`, `nodes_json(prefix)`, `relay_stats_json()` | identity and health |
| `send(to, data, flags, timeout_ms)` | direct message; flags `magnetk::Receipt`, `Store`, `Unreliable` |
| `publish(topic, data, unreliable)` | to every subscriber (`a.b`, `a.*`, `a.>` patterns) |
| `subscribe(pattern, queue="")`, `unsubscribe(pattern)` | `queue` = load-balanced group |
| `serve(topic)` | receive requests sent to `topic` (load-balanced over servers) |
| `request(to, data)`, `request_topic(topic, data)` | returns the reply; throws `RemoteError`, `TimeoutError`, `NoRouteError` |
| `receive(timeout_ms)` → `std::optional<Message>` | pull model |
| `on_message(handler)` | push model on a dispatcher thread; for requests return the reply (`std::nullopt` = no reply); `on_message(nullptr)` stops it |
| `respond(msg, data, is_error)` | answer a request explicitly |
| `watch_presence(on)` | join/leave events arrive as `Kind::Presence` |
| `open_pipe(to, service, meta, route)` → `Pipe` | reliable byte stream, peer-to-peer when possible |
| `accept_pipe(timeout_ms)` → `std::optional<Pipe>` | incoming pipes |
| `share(path)` → `SharedFile{hash,name,size,link,...}` | serve a file; `link` can be given to anyone |
| `unshare(hash)`, `providers_json(hash)` | |
| `download(link, out, progress_cb, route, seed)` → `DownloadResult` | multi-source, verified, resumable |
| `close()` | also done by the destructor |

`Message`: `kind`, `id`, `from`, `subject`, `data` (binary-safe
`std::string`), `datagram`, `request`, `is_request()`.

### `Pipe`

Move-only, closes on destruction. `read(buf, len, timeout_ms)` (0 = EOF),
`read_all()`, `write(data)`, `close_write()` (EOF to the peer), `close()`;
fields `peer`, `service`, `meta`, `direct` (true = peer-to-peer).

### `Relay`

`Relay(RelayOptions{listen, id, tokens, peers, cluster_secret, transport,
log})`, `address()`, `port()`, `stats_json()`, `close()`. For production
use `magnetk relay` as a service ([RELAY_DEPLOYMENT.md](../../docs/RELAY_DEPLOYMENT.md)).

### Errors

Everything throws `magnetk::Error` subclasses with `code()`:
`TimeoutError`, `ClosedError`, `NoRouteError`, `UnauthorizedError`,
`NotConnectedError`, `RemoteError`, `RejectedError`, `NoProvidersError`,
`InvalidError`, `DroppedError`.

## Examples

`examples/chat.cpp`, `rpc.cpp` (server/client), `share.cpp`,
`download.cpp` — built into `build/bin`.

```bash
build/bin/rpc server 127.0.0.1:4002 &   # run several: load-balanced
build/bin/rpc client 127.0.0.1:4002
```

## Threads and resources

All `Node` methods are thread-safe. The engine runs on Go's scheduler
inside libmagnetk; limit it with `MAGNETK_MAX_PROCS` (CPU cores) and
`MAGNETK_MEMORY_LIMIT_MB` (soft heap limit) in the environment.

## Identity and secure chat

```cpp
magnetk::NodeOptions o;
o.relays = {"relay.example.com:4002"};
o.id = "alice";
o.identity_file = "alice.key";          // created on first use; enables secure chat
o.chat_dir = "received";
o.chat_require_approval = true;         // strangers arrive as message requests
magnetk::Node alice(o);

std::cout << alice.identity_json() << "\n";          // {"fingerprint","formatted","address"}
alice.chat_send("bob", "hello");                      // end-to-end encrypted + signed
alice.chat_send("bob", "listen", wav_bytes, "audio/wav");
while (auto m = alice.receive(1000)) {
    if (m->is_chat()) std::cout << m->subject << " " << m->data << "\n";   // data = JSON
}
std::cout << alice.safety_number("bob") << "\n";      // compare with bob, then:
alice.mark_verified("bob");
alice.chat_allow("carol");                            // accept a message request
```

The chat JSON has `text`, `path` (saved attachment), `encrypted`, `trust`
(`verified`|`new`|`changed`|`unsigned`|`invalid`), `request`, `fingerprint`,
`rtt_ms` (acks). Model and limits: [SECURITY.md](../../docs/SECURITY.md).
