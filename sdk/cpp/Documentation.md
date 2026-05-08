# Magnetk C++ SDK Documentation

The C++ SDK is a lightweight wrapper around the native Go `seed` and `peer` binaries, with C++ link parsing and progress callbacks. It is useful when a C++ application wants to launch Magnetk transfers without reimplementing the full relay/libp2p/MsQuic stack.

Current behavior:

- `Seed` starts a native `seed` process and keeps it alive.
- `Client` starts a native `peer` process and parses progress logs.
- Magnet links are parsed and generated in C++.
- CMake builds the SDK, test, and examples.

For fully embedded native transfer without spawning binaries, more C transport work would be needed. Today, the wrapper path is the supported path.

## Build

```powershell
cmake -S sdk\cpp -B sdk\cpp\build-magnetk
cmake --build sdk\cpp\build-magnetk
sdk\cpp\build-magnetk\Debug\test_sdk.exe
```

CMake builds:

- `magnetk_cpp.lib`
- `test_sdk.exe`
- `cpp_share.exe`
- `cpp_download.exe`

On Windows, CMake copies `magnetk_transport.dll` and `msquic.dll` beside the executables.

## Requirements

`Seed` and `Client` expect the native binaries to be available:

- `seed` or `seed.exe`
- `peer` or `peer.exe`

Either put them in `PATH`, or pass explicit paths:

```cpp
magnetk::Seed::Config cfg;
cfg.seed_path = "D:\\More_Projects\\Projects\\Magnetk\\bin\\seed.exe";

magnetk::Client::Config ccfg;
ccfg.peer_path = "D:\\More_Projects\\Projects\\Magnetk\\bin\\peer.exe";
```

## Share A File

```cpp
#include "magnetk.hpp"
#include <iostream>

int main() {
    magnetk::Seed::Config cfg;
    cfg.relay_addr = "69.169.109.243:4003";
    cfg.port = 4002;
    cfg.hole_punch = true;
    cfg.relay_assisted = "auto";
    cfg.transport_mode = "auto";
    cfg.seed_path = "seed";

    magnetk::Seed seed(cfg);
    std::string link = seed.publish("D:\\MUSIC\\el.mp4");

    std::cout << link << std::endl;
    std::cin.get();
}
```

Example:

```powershell
sdk\cpp\build-magnetk\Debug\cpp_share.exe D:\MUSIC\el.mp4
```

The `Seed` destructor stops the spawned seed process.

## Download A File

```cpp
#include "magnetk.hpp"
#include <iostream>

int main() {
    magnetk::Client::Config cfg;
    cfg.relay_assisted = "auto";
    cfg.hole_punch = true;
    cfg.transport_mode = "auto";
    cfg.peer_path = "peer";

    magnetk::Client client(cfg);
    client.download("magnetk:?...", "./downloads/", [](const magnetk::Progress& p) {
        std::cout << "\r" << p.percent << "% "
                  << p.done_chunks << "/" << p.total_chunks
                  << " chunks " << p.speed_mbps << " MB/s" << std::flush;
    });
}
```

Example:

```powershell
sdk\cpp\build-magnetk\Debug\cpp_download.exe "magnetk:?..." .\downloads\
```

## Config Types

### `Seed::Config`

```cpp
struct Config {
    std::string bind_address;
    uint16_t    port = 4002;
    std::string cert_file;
    std::string key_file;
    std::string relay_addr = "69.169.109.243:4003";
    std::string relay_assisted = "auto";
    std::string transport_mode = "auto";
    bool        hole_punch = true;
    bool        dht_enabled = false;
    std::string seed_path = "seed";
    std::string config_path;
};
```

### `Client::Config`

```cpp
struct Config {
    std::string bind_address;
    uint16_t    max_conns = 10;
    bool        no_cert_validation = true;
    std::string transport_mode = "auto";
    std::string relay_assisted = "auto";
    bool        hole_punch = true;
    std::string peer_path = "peer";
    std::string config_path;
};
```

## Progress

```cpp
struct Progress {
    std::string file_name;
    int64_t     file_size;
    int64_t     downloaded;
    uint32_t    total_chunks;
    uint32_t    done_chunks;
    double      percent;
    double      speed_mbps;
};
```

The SDK parses the native `peer` output. It reports both completed chunk progress and in-progress chunk receiving lines when available.

## Magnet Links

```cpp
auto ml = magnetk::MagnetLink::parse("magnetk:?...");

std::cout << ml.file_hash << std::endl;
std::cout << ml.file_name << std::endl;
std::cout << ml.file_size << std::endl;
std::cout << ml.seed_id << std::endl;
std::cout << ml.relay_addr << std::endl;

std::string uri = ml.to_string();
```

The C++ parser URL-decodes `relay=` and `dn=`, and `to_string()` URL-encodes them again.

## Relay-Assisted Modes

- `"auto"`: native peer tries direct paths, then relay fallback.
- `"true"`: relay-assisted transfer is allowed.
- `"false"`: direct P2P only; fail if direct cannot be established.

On mobile hotspots or carrier NAT, direct P2P often fails, so `"auto"` is usually the practical setting.

## Method Reference

### `Seed seed(config)`

Constructs a wrapper. No process starts until `publish()`.

### `seed.publish(file_path)`

Starts native `seed`, waits for `Magnetk Link:`, returns the link, and keeps the process alive.

### `Client client(config)`

Constructs a download wrapper.

### `client.download(magnet_uri, output_path, callback)`

Runs native `peer`, streams output, calls the progress callback, and throws if `peer` exits with a non-zero status.

## Verify

```powershell
cmake -S sdk\cpp -B sdk\cpp\build-magnetk
cmake --build sdk\cpp\build-magnetk
sdk\cpp\build-magnetk\Debug\test_sdk.exe
```
