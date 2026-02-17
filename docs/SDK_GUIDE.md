# Magnetk SDK Guide

This guide explains how to use the Magnetk P2P SDKs for Go, JavaScript (Node.js), and C++.

## 1. Go SDK

The Go SDK provides the most complete implementation, supporting both seeding and downloading via MsQuic transport.

### Installation
```bash
go get github.com/iMarand/magnetk/sdk/go/magnetk
```

### Hosting a File (Seed)
```go
package main

import (
    "fmt"
    "github.com/iMarand/magnetk/sdk/go/magnetk"
)

func main() {
    // Initialize seed node
    seed, err := magnetk.NewSeed(magnetk.SeedConfig{
        RelayAddr:     "/ip4/127.0.0.1/tcp/4001/p2p/<relay_id>",
        TransportPort: 4002,
    })
    if err != nil {
        panic(err)
    }
    defer seed.Close()

    // Publish file
    link, err := seed.PublishFile("my_video.mp4")
    if err != nil {
        panic(err)
    }

    fmt.Println("Share this link:", link)
    
    // Keep running to serve chunks
    select {}
}
```

### Downloading a File (Client)
```go
import "github.com/iMarand/magnetk/sdk/go/magnetk"

client, _ := magnetk.NewClient(magnetk.ClientConfig{})
err := client.Download(magnetLink, "./downloads/", func(p magnetk.Progress) {
    fmt.Printf("Progress: %.1f%%\n", p.Percent)
})
```

---

## 2. JavaScript SDK (Node.js)

The JS SDK supports connecting to the Magnetk network using TCP. It implements the binary framing protocol but relies on seeds supporting TCP fallback or a proxy for data transfer (MsQuic binding coming soon).

### Installation
```bash
cd sdk/js/magnetk
npm install
```

### Usage
```javascript
import { MagnetkClient, MagnetkLink } from 'magnetk-sdk-js';

const client = new MagnetkClient();

// Connect and download (metadata only for now without C++ bindings)
await client.download('magnetk:?xt=urn:sha256:...', './output');
```

---

## 3. C++ SDK

The C++ SDK offers high-performance integration directly with the C MsQuic transport layer.

### build
```bash
mkdir build && cd build
cmake ..
make
```

### Usage
```cpp
#include "magnetk.hpp"

int main() {
    magnetk::Client client;
    
    client.download(
        "magnetk:?xt=urn:sha256:...", 
        "./output.dat",
        [](const magnetk::Progress& p) {
            std::cout << p.percent << "%" << std::endl;
        }
    );
    
    return 0;
}
```


## 4. Advanced Configuration & DHT

To enable decentralized discovery via DHT, you can configure your `settings.conf` file (default location: `./config/settings.conf`).

### Enabling DHT
```ini
# Enable DHT discovery
DHT_ENABLED=true

# Discovery Mode: "server" (for seeds/relays), "client" (for downloaders), "auto"
DHT_MODE=auto

# Optional: Custom Bootstrap Nodes (comma-separated multiaddrs)
# Leave empty to use default IPFS bootstrap nodes
DHT_BOOTSTRAP_PEERS=
```

### Discovery Priority
You can control the order in which the SDK attempts to find a file's location:
```ini
# Try DHT first, then Relay, then Direct connection
DISCOVERY_PRIORITY=dht,relay,direct
```

### Redundancy & Failover
To ensure high availability, you can configure multiple relays. If one goes down, the SDK automatically switches to the next one.
```ini
RELAY_ADDRESSES=/ip4/1.2.3.4/tcp/4001/p2p/Qm...,/ip4/5.6.7.8/tcp/4001/p2p/Qm...
```

### Performance Tuning
- **Adaptive Chunking**: Enabled by default (`ADAPTIVE_CHUNKING=true`), it adjusts chunk sizes based on network conditions.
- **Parallel Downloads**: `MAX_PARALLEL_CHUNKS` (default 10) controls concurrency.
- **Buffers**: `SEND_BUFFER_SIZE_MB` and `RECV_BUFFER_SIZE_MB` (default 4MB) optimize throughput for high-latency links.

## Protocol Overview

All SDKs speak the **Magnetk Binary Framing Protocol**:

- **Header**: 4 bytes Length + 1 byte Type
- **Control Messages** (Manifest, Handshake): JSON payload
- **Data Messages** (Chunks): Raw binary payload (Zero-Copy)

This hybrid approach allows easy debugging (JSON) while maintaining high throughput for file data.
