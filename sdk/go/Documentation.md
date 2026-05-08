# Magnetk Go SDK Documentation

The Go SDK is the most complete SDK. It uses Magnetk packages directly and supports the same network paths as the CLI:

- Direct MsQuic data transfer
- TCP fallback
- Relay discovery through a public relay
- libp2p circuit-relay fallback
- libp2p QUIC/DCUtR hole-punch attempts
- UPnP/NAT-PMP port mapping where the router allows it

Use the Go SDK when you want native Magnetk behavior inside a Go application without spawning the CLI binaries.

## Install

From this repository, the SDK module is at:

```powershell
cd sdk\go
go test ./...
```

The SDK uses the root Go module through:

```go
replace magnetk => ../../go
```

## Basic Sharing

```go
package main

import (
	"fmt"
	"log"

	"magnetk/sdk/go/magnetk"
)

func main() {
	seed, err := magnetk.NewSeed(magnetk.SeedConfig{
		RelayAddr:     "69.169.109.243:4003",
		TransportPort: 4002,
		HolePunch:     true,
		RelayAssisted: "auto",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer seed.Close()

	link, err := seed.PublishFile("D:\\MUSIC\\el.mp4")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(link)
	select {}
}
```

`RelayAddr` can be either:

```text
69.169.109.243:4003
```

or a full relay multiaddr:

```text
/ip4/69.169.109.243/tcp/4001/p2p/<RELAY_PEER_ID>
```

If you pass `host:4003`, the SDK asks the relay for its Peer ID and builds the proper libp2p multiaddr.

## Basic Downloading

```go
package main

import (
	"fmt"
	"log"

	"magnetk/sdk/go/magnetk"
)

func main() {
	client, err := magnetk.NewClient(magnetk.ClientConfig{
		HolePunch:     true,
		RelayAssisted: "auto",
		TransportMode: "auto",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	err = client.Download("magnetk:?...", "./downloads", func(p magnetk.Progress) {
		fmt.Printf("\r%.1f%% %.2f MB/s %d/%d chunks",
			p.Percent, p.SpeedMBps, p.DoneChunks, p.TotalChunks)
	})
	if err != nil {
		log.Fatal(err)
	}
}
```

`Download` accepts either an output directory or a full output file path.

## Config Types

### `SeedConfig`

```go
type SeedConfig struct {
	RelayAddr         string
	BindAddress       string
	AdvertisedAddress string
	TransportPort     int
	ChunkSizeMB       int
	ConfigPath        string
	DHTEnabled        bool
	DHTBootstrapPeers []string
	RelayAssisted     string
	HolePunch         bool
	TransportMode     string
	RelayAddresses    []string
}
```

Important fields:

- `RelayAddr`: public relay address. Recommended for cross-network discovery.
- `TransportPort`: seed data port. Default is `4002`.
- `AdvertisedAddress`: public IP to publish when you manually port-forward.
- `RelayAssisted`: `"auto"`, `"true"`, or `"false"`.
- `HolePunch`: enables libp2p hole punching and NAT port mapping attempts.
- `TransportMode`: `"auto"`, `"quic"`, or `"tcp"`.

### `ClientConfig`

```go
type ClientConfig struct {
	BindAddress       string
	OutputDir         string
	MaxConnections    int
	ConfigPath        string
	DHTEnabled        bool
	DHTBootstrapPeers []string
	RelayAssisted     string
	HolePunch         bool
	TransportMode     string
	RelayAddresses    []string
}
```

## Relay-Assisted Modes

`RelayAssisted` controls whether data may travel through the relay when direct P2P fails.

- `"auto"`: try direct first, fall back to relay.
- `"true"`: allow relay path. Direct may still be tried first depending on the caller path.
- `"false"`: direct P2P only. If hole punching/direct dial fails, download fails.

On mobile hotspots and carrier NAT, direct hole punching often fails. In that case `"auto"` or `"true"` is needed.

## Method Reference

### `NewSeed(config SeedConfig) (*Seed, error)`

Starts a seed node with:

- MsQuic listener
- TCP fallback listener
- libp2p host
- optional relay reservation
- optional NAT mapping

### `(*Seed) PublishFile(filePath string) (string, error)`

Chunks the file, registers it with the relay, and returns a `magnetk:?` link.

### `(*Seed) Close()`

Stops listeners, closes libp2p, transport, and NAT mappings.

### `NewClient(config ClientConfig) (*Client, error)`

Creates a download client.

### `(*Client) Download(magnetURI, outputDir string, cb ProgressCallback) error`

Discovers seeds, tries direct MsQuic/TCP, then libp2p relay fallback if allowed.

### `ParseMagnetLink(uri string)`

Parses a Magnetk link into its file hash, size, seed ID, relay address, and DHT flag.

## Network Notes

Same Wi-Fi usually uses direct LAN addresses.

Different routers may use direct P2P if:

- UPnP/NAT-PMP opens the seed port, or
- manual port forwarding sends TCP/UDP `4002` to the seed, or
- libp2p hole punching succeeds.

Different networks with strict NAT, mobile hotspot, or carrier NAT usually need relay-assisted mode.

## Verify

```powershell
cd sdk\go
go test ./...
```
