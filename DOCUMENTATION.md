# Magnetk Documentation

## 1. Installation & Build

### Prerequisites
- **Go**: v1.20+
- **GCC**:
  - **Windows**: MinGW-w64 (Must use `mingw64` toolchain, NOT MSYS2/POSIX)
  - **Linux**: standard `gcc` (`build-essential`)
  - **macOS**: Xcode Command Line Tools
- **CMake**: v3.16+ (for C++ SDK)
- **Node.js**: v16+ (for JS SDK)

### Cloning the Repository
```bash
git clone https://github.com/iMarand/magnetk.git
cd magnetk
```

### Building Core Components (Relay/Seed/Peer)

#### Windows (PowerShell)
1. **Ensure MinGW64 is in PATH**:
   ```powershell
   $env:Path = "C:\msys64\mingw64\bin;" + $env:Path
   ```
2. **Build**:
   ```powershell
   ./scripts/build.bat
   ```

#### Linux / macOS
```bash
chmod +x scripts/build.sh
./scripts/build.sh
```

---

## 2. Using the SDKs

### Go SDK
Ideal for building high-performance P2P applications with direct native transport access.

**Installation**:
```go
import "github.com/iMarand/magnetk/sdk/go/magnet"
```

**Usage**:
```go
cfg := magnetk.DefaultConfig()
client, err := magnetk.NewClient(cfg)
if err != nil {
    log.Fatal(err)
}

// Download a file
err = client.Download("magnet:?xt=urn:sha256:...", "./output.file")
```

### JavaScript SDK (Node.js)
Perfect for web services or Electron apps. Connects to the relay/p2p network via WebSocket or TCP bridge.

**Installation**:
```bash
cd sdk/js/magnet
npm install
```

**Usage**:
```javascript
import { MagnetkClient } from './client.js';

const client = new MagnetkClient();
await client.download('magnet:?xt=urn:sha256:...', './downloads');
```

### C++ SDK
For low-level integration or embedded systems.

**Build**:
```bash
cd sdk/cpp
mkdir build && cd build
cmake ..
make
```

**Usage**:
```cpp
#include "magnetk_sdk.h"

magnetk_client_t* client = magnetk_client_init();
magnetk_client_download(client, "magnet:?xt=...", "./out.dat");
```

---

## 3. Performance & Architecture

### Faster File Transfers
Magnetk P2P is faster than traditional HTTP for large files because:
1.  **QUIC Transport**: Uses UDP to avoid Head-of-Line blocking.
2.  **Parallel Chunking**: Downloads multiple chunks simultaneously from different peers.
3.  **Relay Buffering**: The relay caches chunks for offline peers, smoothing out speed variations.

### Comparison
| Feature | Magnetk P2P | Classic HTTP | BitTorrent |
| :--- | :--- | :--- | :--- |
| Transport | QUIC (UDP) | TCP | TCP/UDP |
| Latency | Low (0-RTT) | Medium | High |
| Resilience | High (Resume) | Medium | High |
| Centralization | Semi (Relay) | Yes (Server) | No (DHT) |


## 4. Troubleshooting
- **Build Fails on Windows**: Ensure you are using **MinGW-w64** GCC. If `go build` says `exit status 2`, you are likely using MSYS2 POSIX GCC.
- **Relay Connection Refused**: Check if `bin/relay` is running and the port (default 4001) is open.
