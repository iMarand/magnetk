// Package discovery handles peer discovery using libp2p
package discovery

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"

	"magnet/pkg/types"
)

// Node represents a libp2p node in the Magnet network
type Node struct {
	host       host.Host
	ctx        context.Context
	cancel     context.CancelFunc
	peers      map[string]*types.PeerInfo
	peersMux   sync.RWMutex
	isRelay    bool
	relayAddrs []multiaddr.Multiaddr
}

// NodeConfig contains configuration for creating a Node
type NodeConfig struct {
	ListenPort int
	IsRelay    bool
	RelayAddrs []string
	EnableQUIC bool
}

// NewNode creates a new libp2p node
func NewNode(cfg NodeConfig) (*Node, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Build listen addresses
	listenAddrs := make([]multiaddr.Multiaddr, 0)

	// TCP address
	tcpAddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", cfg.ListenPort))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create TCP multiaddr: %w", err)
	}
	listenAddrs = append(listenAddrs, tcpAddr)

	// QUIC address if enabled
	if cfg.EnableQUIC {
		quicAddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", cfg.ListenPort))
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to create QUIC multiaddr: %w", err)
		}
		listenAddrs = append(listenAddrs, quicAddr)
	}

	// Build libp2p options
	opts := []libp2p.Option{
		libp2p.ListenAddrs(listenAddrs...),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Security(noise.ID, noise.New),
	}

	if cfg.EnableQUIC {
		opts = append(opts, libp2p.Transport(libp2pquic.NewTransport))
	}

	// Create host
	h, err := libp2p.New(opts...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create libp2p host: %w", err)
	}

	// Parse relay addresses
	relayAddrs := make([]multiaddr.Multiaddr, 0, len(cfg.RelayAddrs))
	for _, addr := range cfg.RelayAddrs {
		ma, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			h.Close()
			cancel()
			return nil, fmt.Errorf("failed to parse relay address %s: %w", addr, err)
		}
		relayAddrs = append(relayAddrs, ma)
	}

	return &Node{
		host:       h,
		ctx:        ctx,
		cancel:     cancel,
		peers:      make(map[string]*types.PeerInfo),
		isRelay:    cfg.IsRelay,
		relayAddrs: relayAddrs,
	}, nil
}

// Start starts the node and connects to relays
func (n *Node) Start() error {
	fmt.Printf("Node started with ID: %s\n", n.host.ID())
	fmt.Println("Listening on:")
	for _, addr := range n.host.Addrs() {
		fmt.Printf("  %s/p2p/%s\n", addr, n.host.ID())
	}

	// Connect to relay nodes if not a relay
	if !n.isRelay && len(n.relayAddrs) > 0 {
		for _, addr := range n.relayAddrs {
			go n.connectToRelay(addr)
		}
	}

	return nil
}

// connectToRelay attempts to connect to a relay node
func (n *Node) connectToRelay(addr multiaddr.Multiaddr) {
	// Extract peer ID from multiaddr
	peerInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		fmt.Printf("Failed to parse relay address: %v\n", err)
		return
	}

	n.host.Peerstore().AddAddrs(peerInfo.ID, peerInfo.Addrs, peerstore.PermanentAddrTTL)

	ctx, cancel := context.WithTimeout(n.ctx, 30*time.Second)
	defer cancel()

	if err := n.host.Connect(ctx, *peerInfo); err != nil {
		fmt.Printf("Failed to connect to relay %s: %v\n", peerInfo.ID, err)
		return
	}

	fmt.Printf("Connected to relay: %s\n", peerInfo.ID)
}

// Stop stops the node
func (n *Node) Stop() error {
	n.cancel()
	return n.host.Close()
}

// Host returns the libp2p host
func (n *Node) Host() host.Host {
	return n.host
}

// ID returns the peer ID as a string
func (n *Node) ID() string {
	return n.host.ID().String()
}

// Addrs returns the node's listen addresses
func (n *Node) Addrs() []string {
	addrs := make([]string, 0, len(n.host.Addrs()))
	for _, addr := range n.host.Addrs() {
		addrs = append(addrs, addr.String())
	}
	return addrs
}

// RegisterPeer adds or updates a peer in the registry
func (n *Node) RegisterPeer(info *types.PeerInfo) {
	n.peersMux.Lock()
	defer n.peersMux.Unlock()

	info.LastSeen = time.Now()
	info.Online = true
	n.peers[info.PeerID] = info
}

// UnregisterPeer removes a peer from the registry
func (n *Node) UnregisterPeer(peerID string) {
	n.peersMux.Lock()
	defer n.peersMux.Unlock()
	delete(n.peers, peerID)
}

// GetPeer retrieves a peer by ID
func (n *Node) GetPeer(peerID string) *types.PeerInfo {
	n.peersMux.RLock()
	defer n.peersMux.RUnlock()
	return n.peers[peerID]
}

// ListPeers returns all registered peers
func (n *Node) ListPeers() []*types.PeerInfo {
	n.peersMux.RLock()
	defer n.peersMux.RUnlock()

	peers := make([]*types.PeerInfo, 0, len(n.peers))
	for _, p := range n.peers {
		peers = append(peers, p)
	}
	return peers
}

// MarkPeerOffline marks a peer as offline
func (n *Node) MarkPeerOffline(peerID string) {
	n.peersMux.Lock()
	defer n.peersMux.Unlock()

	if p, ok := n.peers[peerID]; ok {
		p.Online = false
	}
}

// UpdatePeerHeartbeat updates the last seen time for a peer
func (n *Node) UpdatePeerHeartbeat(peerID string) {
	n.peersMux.Lock()
	defer n.peersMux.Unlock()

	if p, ok := n.peers[peerID]; ok {
		p.LastSeen = time.Now()
		p.Online = true
	}
}

// Connect connects to a peer by their multiaddr
func (n *Node) Connect(ctx context.Context, addrStr string) (peer.ID, error) {
	addr, err := multiaddr.NewMultiaddr(addrStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse address: %w", err)
	}

	peerInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return "", fmt.Errorf("failed to extract peer info: %w", err)
	}

	n.host.Peerstore().AddAddrs(peerInfo.ID, peerInfo.Addrs, peerstore.TempAddrTTL)

	if err := n.host.Connect(ctx, *peerInfo); err != nil {
		return "", fmt.Errorf("failed to connect: %w", err)
	}

	return peerInfo.ID, nil
}
