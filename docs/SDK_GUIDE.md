# Magnet SDK Guide

This guide explains how to use the Magnet P2P SDKs for Go, JavaScript (Node.js), and C++.

## 1. Go SDK

The Go SDK provides the most complete implementation, supporting both seeding and downloading via MsQuic transport.

### Installation
```bash
go get github.com/iMarand/magnetk/sdk/go/magnet
```

### Hosting a File (Seed)
```go
package main

import (
    "fmt"
    "github.com/iMarand/magnetk/sdk/go/magnet"
)

func main() {
    // Initialize seed node
    seed, err := magnet.NewSeed(magnet.SeedConfig{
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
import "github.com/iMarand/magnetk/sdk/go/magnet"

client, _ := magnet.NewClient(magnet.ClientConfig{})
err := client.Download(magnetLink, "./downloads/", func(p magnet.Progress) {
    fmt.Printf("Progress: %.1f%%\n", p.Percent)
})
```

---

## 2. JavaScript SDK (Node.js)

The JS SDK supports connecting to the Magnet network using TCP. It implements the binary framing protocol but relies on seeds supporting TCP fallback or a proxy for data transfer (MsQuic binding coming soon).

### Installation
```bash
cd sdk/js/magnet
npm install
```

### Usage
```javascript
import { MagnetClient, MagnetLink } from 'magnet-sdk-js';

const client = new MagnetClient();

// Connect and download (metadata only for now without C++ bindings)
await client.download('magnet:?xt=urn:sha256:...', './output');
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
#include "magnet.hpp"

int main() {
    magnet::Client client;
    
    client.download(
        "magnet:?xt=urn:sha256:...", 
        "./output.dat",
        [](const magnet::Progress& p) {
            std::cout << p.percent << "%" << std::endl;
        }
    );
    
    return 0;
}
```

## Protocol Overview

All SDKs speak the **Magnet Binary Framing Protocol**:

- **Header**: 4 bytes Length + 1 byte Type
- **Control Messages** (Manifest, Handshake): JSON payload
- **Data Messages** (Chunks): Raw binary payload (Zero-Copy)

This hybrid approach allows easy debugging (JSON) while maintaining high throughput for file data.
