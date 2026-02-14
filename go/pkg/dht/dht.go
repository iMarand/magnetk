// Package dht provides Kademlia DHT-based decentralized peer discovery for Magnetk.
//
// It wraps go-libp2p-kad-dht to allow seeds to announce file availability
// and peers to discover seeds without a central relay server.
//
// Two modes:
//   - Server mode: for seeds and relays (serves DHT requests + stores records)
//   - Client mode: for downloaders (query-only, lightweight)
package dht

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// DefaultBootstrapPeers are public IPFS bootstrap nodes used when no custom ones are configured.
var DefaultBootstrapPeers = []string{
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
	"/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ",
}

// Config holds DHT configuration.
type Config struct {
	// Mode: "server" (seeds/relays), "client" (downloaders), "auto" (decide based on usage)
	Mode string

	// BootstrapPeers: multiaddrs of bootstrap nodes to join the DHT network.
	// If empty, DefaultBootstrapPeers are used.
	BootstrapPeers []string

	// ReannounceInterval: how often to re-announce provided content.
	// Default: 30 minutes.
	ReannounceInterval time.Duration
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Mode:               "auto",
		BootstrapPeers:     nil, // Will use DefaultBootstrapPeers
		ReannounceInterval: 30 * time.Minute,
	}
}

// DHT wraps a Kademlia DHT instance for Magnetk discovery.
type DHT struct {
	host    host.Host
	kdht    *dht.IpfsDHT
	cfg     *Config
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	stopChs []chan struct{} // channels to stop re-announce goroutines
}

// New creates and initializes a new DHT instance attached to the given libp2p host.
// The host should already be listening (e.g., on port 4001).
func New(h host.Host, cfg *Config) (*DHT, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Determine DHT mode
	var dhtMode dht.ModeOpt
	switch cfg.Mode {
	case "server":
		dhtMode = dht.ModeServer
	case "client":
		dhtMode = dht.ModeClient
	default:
		dhtMode = dht.ModeAutoServer
	}

	// Create the Kademlia DHT
	kdht, err := dht.New(ctx, h, dht.Mode(dhtMode))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create DHT: %w", err)
	}

	d := &DHT{
		host:   h,
		kdht:   kdht,
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}

	return d, nil
}

// Bootstrap connects to bootstrap peers and initializes the DHT routing table.
// This must be called before Announce or FindProviders will work.
func (d *DHT) Bootstrap() error {
	// Determine which bootstrap peers to use
	bootstrapAddrs := d.cfg.BootstrapPeers
	if len(bootstrapAddrs) == 0 {
		bootstrapAddrs = DefaultBootstrapPeers
	}

	log.Printf("[dht] Bootstrapping with %d peers...", len(bootstrapAddrs))

	// Connect to each bootstrap peer
	var wg sync.WaitGroup
	connected := 0
	var connMu sync.Mutex

	for _, addrStr := range bootstrapAddrs {
		ma, err := multiaddr.NewMultiaddr(addrStr)
		if err != nil {
			log.Printf("[dht] Warning: invalid bootstrap addr %s: %v", addrStr, err)
			continue
		}

		peerInfo, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			log.Printf("[dht] Warning: cannot extract peer info from %s: %v", addrStr, err)
			continue
		}

		wg.Add(1)
		go func(pi peer.AddrInfo) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
			defer cancel()

			if err := d.host.Connect(ctx, pi); err != nil {
				log.Printf("[dht] Warning: failed to connect to bootstrap peer %s: %v", pi.ID.String()[:12], err)
				return
			}

			connMu.Lock()
			connected++
			connMu.Unlock()
			log.Printf("[dht] Connected to bootstrap peer: %s", pi.ID.String()[:12])
		}(*peerInfo)
	}

	wg.Wait()
	log.Printf("[dht] Connected to %d/%d bootstrap peers", connected, len(bootstrapAddrs))

	// Bootstrap the DHT routing table
	if err := d.kdht.Bootstrap(d.ctx); err != nil {
		return fmt.Errorf("DHT bootstrap failed: %w", err)
	}

	log.Println("[dht] DHT routing table bootstrapped successfully")
	return nil
}

// Announce registers the local peer as a provider for the given file hash.
// It also starts a background goroutine that re-announces periodically.
func (d *DHT) Announce(fileHash string) error {
	c, err := hashToCID(fileHash)
	if err != nil {
		return fmt.Errorf("failed to create CID from hash: %w", err)
	}

	// Initial announcement
	if err := d.kdht.Provide(d.ctx, c, true); err != nil {
		return fmt.Errorf("DHT provide failed: %w", err)
	}
	log.Printf("[dht] Announced as provider for %s", fileHash[:12])

	// Start re-announcement goroutine
	stopCh := make(chan struct{})
	d.mu.Lock()
	d.stopChs = append(d.stopChs, stopCh)
	d.mu.Unlock()

	go func() {
		interval := d.cfg.ReannounceInterval
		if interval <= 0 {
			interval = 30 * time.Minute
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := d.kdht.Provide(d.ctx, c, true); err != nil {
					log.Printf("[dht] Re-announce failed for %s: %v", fileHash[:12], err)
				} else {
					log.Printf("[dht] Re-announced as provider for %s", fileHash[:12])
				}
			case <-stopCh:
				return
			case <-d.ctx.Done():
				return
			}
		}
	}()

	return nil
}

// FindProviders queries the DHT for peers that have announced the given file hash.
// Returns a slice of peer.AddrInfo with addresses for each provider found.
// timeout controls how long to search; 0 means use a default of 30 seconds.
func (d *DHT) FindProviders(fileHash string, timeout time.Duration) ([]peer.AddrInfo, error) {
	c, err := hashToCID(fileHash)
	if err != nil {
		return nil, fmt.Errorf("failed to create CID from hash: %w", err)
	}

	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	ctx, cancel := context.WithTimeout(d.ctx, timeout)
	defer cancel()

	log.Printf("[dht] Searching for providers of %s...", fileHash[:12])

	providersChan := d.kdht.FindProvidersAsync(ctx, c, 20)

	var providers []peer.AddrInfo
	for pi := range providersChan {
		if pi.ID == d.host.ID() {
			continue // Skip self
		}
		providers = append(providers, pi)
		log.Printf("[dht] Found provider: %s (%d addrs)", pi.ID.String()[:12], len(pi.Addrs))
	}

	if len(providers) == 0 {
		return nil, fmt.Errorf("no providers found for %s via DHT", fileHash[:12])
	}

	log.Printf("[dht] Found %d provider(s) for %s", len(providers), fileHash[:12])
	return providers, nil
}

// RoutingTableSize returns the number of peers in the DHT routing table.
func (d *DHT) RoutingTableSize() int {
	return d.kdht.RoutingTable().Size()
}

// Close shuts down the DHT and stops all re-announcement goroutines.
func (d *DHT) Close() error {
	d.mu.Lock()
	for _, ch := range d.stopChs {
		close(ch)
	}
	d.stopChs = nil
	d.mu.Unlock()

	d.cancel()
	return d.kdht.Close()
}

// hashToCID converts a hex-encoded SHA-256 file hash to a CID for DHT storage.
func hashToCID(hexHash string) (cid.Cid, error) {
	// Decode hex string to raw bytes
	raw := make([]byte, len(hexHash)/2)
	for i := 0; i < len(raw); i++ {
		_, err := fmt.Sscanf(hexHash[i*2:i*2+2], "%02x", &raw[i])
		if err != nil {
			return cid.Undef, fmt.Errorf("invalid hex hash: %w", err)
		}
	}

	// Create multihash from raw SHA-256 digest
	multihash, err := mh.Encode(raw, mh.SHA2_256)
	if err != nil {
		return cid.Undef, fmt.Errorf("failed to encode multihash: %w", err)
	}

	// Create CID v1 with raw codec
	return cid.NewCidV1(cid.Raw, multihash), nil
}
