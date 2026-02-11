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
import "github.com/iMarand/magnetk/sdk/go/magnetk"
```

**Usage**:
```go
cfg := magnetk.DefaultConfig()
client, err := magnetk.NewClient(cfg)
if err != nil {
    log.Fatal(err)
}

// Download a file
err = client.Download("magnetk:?xt=urn:sha256:...", "./output.file")
```

### JavaScript SDK (Node.js)
Perfect for web services or Electron apps. Connects to the relay/p2p network via WebSocket or TCP bridge.

**Installation**:
```bash
cd sdk/js/magnetk
npm install
```

**Usage**:
```javascript
import { MagnetkClient } from './client.js';

const client = new MagnetkClient();
await client.download('magnetk:?xt=urn:sha256:...', './downloads');
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
#include "magnetk.hpp"

magnetk::Client client;
client.download("magnetk:?xt=...", "./out.dat");
```

---

## 3. Performance & Architecture
 
### Hybrid Protocol Architecture
Magnetk uses a hybrid approach to balance extreme performance with universal compatibility.
 
| Job | Protocol | Port | Used By | Description |
| :--- | :--- | :--- | :--- | :--- |
| **Discovery** | TCP (LibP2P) | 4001 | Go/Python Nodes | COORDINATION: Registry & peer routing. |
| **JS Discovery**| TCP (Raw) | 4003 | JS SDK | COORDINATION: Fallback for lighter clients. |
| **Data (Fast)** | **QUIC (UDP)**| 4002 | Go/Python/C++ | TRANSFER: High-speed, 0-RTT, parallel. |
| **Data (JS)** | TCP (Raw) | 4002 | JS SDK | TRANSFER: Cross-platform fallback. |
 
### Why QUIC + UDP?
High-speed SDKs (Go, Python, C++) use **MsQuic** over UDP for core data transfer to avoid "Head-of-Line" blocking. This allows the system to remain fast even on high-latency or unstable networks.
 
### Why TCP Fallback?
The JavaScript SDK is designed for maximum compatibility. It uses TCP to ensure it can run in lightweight environments (Node.js, Electron) without requiring complex native driver setups, while still benefiting from the global peer discovery network.
 
### Faster File Transfers
Magnetk P2P is faster than traditional HTTP for large files because:
1.  **Parallel Chunking**: Downloads multiple chunks simultaneously from different peers.
2.  **Relay Buffering**: The relay caches chunks for offline peers, smoothing out speed variations.
 
### Comparison
| Feature | Magnetk P2P | Classic HTTP | BitTorrent |
| :--- | :--- | :--- | :--- |
| Transport | QUIC (UDP) / TCP | TCP | TCP/UDP |
| Latency | Ultra-Low (0-RTT) | Medium | High |
| Resilience | High (Resume) | Medium | High |
| Centralization | Semi (Relay) | Yes (Server) | No (DHT) |


## 4. Troubleshooting
- **Build Fails on Windows**: Ensure you are using **MinGW-w64** GCC. If `go build` says `exit status 2`, you are likely using MSYS2 POSIX GCC.
- **Relay Connection Refused**: Check if `bin/relay` is running and the port (default 4001) is open.
