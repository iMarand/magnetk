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

Magnetk uses a hybrid approach to balance extreme performance with universal compatibility.

| Job | Protocol | Port | Used By | Description |
|-----|----------|------|---------|-------------|
| **Discovery** | TCP (LibP2P) | 4001 | Go/Python Nodes | **COORDINATION**: Registry & peer routing. |
| **JS Discovery** | TCP (Raw) | 4003 | JS SDK | **COORDINATION**: Fallback for lighter clients. |
| **Data (Fast)** | QUIC (UDP) | 4002 | Go/Python/C++ | **TRANSFER**: High-speed, 0-RTT, parallel. |
| **Data (JS)** | TCP (Raw) | 4002 | JS SDK | **TRANSFER**: Cross-platform fallback. |

---

## SDK Usage Examples

### 1. JavaScript (Node.js)

**Share a File (`share.js`)**
```javascript
import { MagnetkClient } from 'magnetk';

async function share() {
    const client = new MagnetkClient({
        // Relay Configuration (Centralized)
        relayUrl: '69.169.109.243',
        relayPort: 4003,

        // DHT Configuration (Decentralized)
        dhtEnabled: true,
        dhtBootstrapPeers: [], // Empty = use default IPFS nodes
        
        // Network
        seedPort: 4002
    });

    try {
        const link = await client.share("./my-file.zip");
        console.log('Magnetk Link:', link);
        await client.keepSeeding();
    } catch (err) {
        console.error(err);
    }
}
share();
```

**Download a File (`download.js`)**
```javascript
import { MagnetkClient } from 'magnetk';

async function download() {
    const client = new MagnetkClient({
        relayUrl: '69.169.109.243',
        relayPort: 4003,
        dhtEnabled: true
    });

    const magnetLink = "magnetk:?xt=urn:sha256:...";
    
    try {
        await client.get(magnetLink, "./downloads/");
        console.log("Download complete!");
    } catch (err) {
        console.error(err);
    }
}
download();
```

### 2. Python

**Share a File (`share.py`)**
```python
from magnetk import MagnetkClient

client = MagnetkClient({
    'relay_url': '69.169.109.243:4003',
    'dht_enabled': True,
    # 'dht_bootstrap_peers': ['/ip4/1.2.3.4/tcp/4001/p2p/...']
})

try:
    link = client.share("./my-video.mp4")
    print(f"Magnetk Link: {link}")
except Exception as e:
    print(f"Error: {e}")
```

**Download a File (`download.py`)**
```python
from magnetk import MagnetkClient

client = MagnetkClient({
    'dht_enabled': True
})

magnet_link = "magnetk:?xt=urn:sha256:..."
try:
    client.get(magnet_link, "./downloads/")
except Exception as e:
    print(f"Error: {e}")
```

### 3. Go

**Share a File (`share.go`)**
```go
package main

import (
	"fmt"
	"magnetk/sdk/go/magnetk"
)

func main() {
	seed, _ := magnetk.NewSeed(magnetk.SeedConfig{
		RelayAddr:  "/ip4/69.169.109.243/tcp/4001/p2p/...",
		DHTEnabled: true,
	})
	
	link, err := seed.PublishFile("my-file.zip")
	if err != nil { panic(err) }
	
	fmt.Println("Magnetk Link:", link)
	select {}
}
```

**Download a File (`download.go`)**
```go
package main

import "magnetk/sdk/go/magnetk"

func main() {
	client, _ := magnetk.NewClient(magnetk.ClientConfig{
		DHTEnabled: true,
	})
	
	err := client.Download("magnetk:?xt=urn:sha256:...", "./downloads/", nil)
	if err != nil { panic(err) }
}
```

### 4. C++

**Share a File (`share.cpp`)**
```cpp
#include "magnetk.hpp"
#include <iostream>

int main() {
    magnetk::Seed::Config cfg;
    cfg.dht_enabled = true;
    cfg.port = 4002;

    magnetk::Seed seed(cfg);
    std::string link = seed.publish("./my-file.zip");
    
    std::cout << "Magnetk Link: " << link << std::endl;
    std::cin.get(); 
    return 0;
}
```

**Download a File (`download.cpp`)**
```cpp
#include "magnetk.hpp"

int main() {
    magnetk::Client::Config cfg;
    cfg.dht_enabled = true;
    
    magnetk::Client client(cfg);
    client.download("magnetk:?xt=urn:sha256:...", "./downloads/");
    
    return 0;
}
```



---

## Configuration (`settings.conf`)

The `settings.conf` file allows you to tune performance and network behavior without changing code. It is automatically generated in the `./config/` directory by the SDKs if missing.


```ini
# Magnetk P2P Configuration Reference
# ===================================
# This file lets you tune every aspect of the Magnetk node.

# 1. NETWORK & DISCOVERY
# ----------------------
# IP to bind to. Use 0.0.0.0 for public, 127.0.0.1 for local-only.
BIND_ADDRESS=0.0.0.0
# Public IP to advertise to peers (leave empty to auto-discover via STUN)
ADVERTISED_ADDRESS=
# TCP port for LibP2P discovery (handshakes, routing)
DISCOVERY_PORT=4001
# QUIC/UDP port for high-speed data transfer
TRANSPORT_PORT=4002
# Discovery priority: dht (decentralized), relay (managed), direct (P2P)
DISCOVERY_PRIORITY=dht,relay,direct

# 2. DHT (Distributed Hash Table)
# -------------------------------
# Enable trackerless decentralized discovery
DHT_ENABLED=true
# Mode: "server" (participate in routing), "client" (leech only), "auto"
DHT_MODE=auto
# Custom bootstrap peers (comma-separated multiaddrs)
# If empty, uses default public IPFS nodes.
DHT_BOOTSTRAP_PEERS=
# How often to announce files to the DHT (minutes)
DHT_REANNOUNCE_INTERVAL_MIN=30

# 3. RELAY (Centralized)
# ----------------------
# Primary relay address (multiaddr format preferred, or host:port)
RELAY_ADDRESS=0.0.0.0:4001
# Fallback relays for redundancy (comma-separated)
RELAY_ADDRESSES=

# 4. PERFORMANCE & TUNING
# -----------------------
# Size of data chunks in MB. Larger = less overhead, but higher latency.
CHUNK_SIZE_MB=1
# Dynamically adjust chunk size based on network speed
ADAPTIVE_CHUNKING=true
# Max concurrent chunk downloads
MAX_PARALLEL_CHUNKS=10
# Max concurrent peer connections
MAX_CONNECTIONS=1000
# Buffer sizes for QUIC streams (increase for high-latency/high-bandwidth)
SEND_BUFFER_SIZE_MB=4
RECV_BUFFER_SIZE_MB=4

# 5. SECURITY & TLS
# -----------------
# Path to TLS certificate/key (PEM). Empty = auto-generated self-signed.
TLS_CERT_FILE=
TLS_KEY_FILE=
# Skip certificate validation (useful for self-signed dev mode)
NO_CERT_VALIDATION=true

# 6. LOGGING & STATE
# ------------------
LOG_LEVEL=info
LOG_FILE=./magnetk.log
# Directory to store resume states
STATE_DIR=./transfer_states
# How long to keep resume state
STATE_EXPIRY_DAYS=7
```


