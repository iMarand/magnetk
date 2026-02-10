# Magnet P2P File Transfer System

A peer-to-peer file transfer network using **libp2p + QUIC** with automatic resume capability, relay-assisted delivery, and multiple SDKs.

## 🚀 Features

- ✅ **Decentralized**: Direct P2P transfers with no central storage server.
- ✅ **Resume Support**: Transfers automatically resume after network interruptions.
- ✅ **QUIC Transport**: High-performance UDP-based connections (via MsQuic) for low latency.
- ✅ **Magnet Links**: Share files easily using standard magnet link format.
- ✅ **Offline Buffering**: Intermediary relays buffer chunks for offline peers.
- ✅ **Cross-Platform SDKs**: Easy integration for Go, JavaScript/Node.js, and C++.

## 📦 Installation

### Prerequisites
- Go 1.20+
- GCC (MinGW-w64 on Windows, `build-essential` on Linux)
- CMake (for C++ SDK)

### Quick Start

#### Windows (PowerShell)
```powershell
# 1. Clone
git clone https://github.com/iMarand/magnetk.git
cd magnetk

# 2. Build
./scripts/build.bat
```

#### Linux / macOS
```bash
# 1. Clone
git clone https://github.com/iMarand/magnetk.git
cd magnetk

# 2. Build
chmod +x scripts/build.sh
./scripts/build.sh
```

## 🛠️ Usage

### 1. Start Relay
Run the relay node on a server/VPS to facilitate connections.
```bash
./bin/relay --port 4001
# Copy the Relay Address from output
```

### 2. Share a File (Seed)
seed a file and get a magnet link.
```bash
./bin/seed --relay "/ip4/127.0.0.1/tcp/4001/p2p/RELAY_ID" --file ./myfile.zip
```

### 3. Download (Peer)
Use the magnet link to download the file.
```bash
./bin/peer --magnet "magnet:?xt=urn:sha256:..." --output ./download.zip
```

## 📚 Documentation
For detailed guides on SDKs, architecture, and advanced configuration, see:
- [**Full Documentation (DOCUMENTATION.md)**](./DOCUMENTATION.md)
- [Architecture Overview](docs/ARCHITECTURE.md)
- [SDK Guide](docs/SDK_GUIDE.md)

## 🔧 SDKs

- **Go SDK**: `sdk/go/magnet` - High performance, native integration.
- **JavaScript SDK**: `sdk/js/magnet` - Web/Node.js integration.
- **C++ SDK**: `sdk/cpp` - Low-level/Embedded integration.

## License
MIT
