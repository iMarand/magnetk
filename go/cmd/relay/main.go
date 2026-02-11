// Relay node for the Magnetk P2P network.
//
// The relay acts as the central coordinator:
// - Discovery: uses libp2p for peer registration and file lookup
// - Transport: uses MsQuic for high-performance data transfer
// - Registry: tracks which seeds have which files
// - Buffer: optionally buffers chunks for offline peers
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"

	"magnetk/pkg/buffer"
	"magnetk/pkg/config"
	"magnetk/pkg/handler"
	"magnetk/pkg/registry"
	"magnetk/pkg/transport"
)

func main() {
	configPath := flag.String("config", "./config/settings.conf", "Path to config file")
	bindAddr := flag.String("bind", "", "Override bind address")
	transportPort := flag.Int("port", 0, "Override transport port")
	discoveryPort := flag.Int("discovery-port", 0, "Override discovery port")
	flag.Parse()

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Printf("[relay] Warning: Could not load config from %s: %v (using defaults)", *configPath, err)
		cfg = config.DefaultConfig()
	}

	// Apply overrides
	if *bindAddr != "" {
		cfg.BindAddress = *bindAddr
	}
	if *transportPort > 0 {
		cfg.TransportPort = *transportPort
	}
	if *discoveryPort > 0 {
		cfg.DiscoveryPort = *discoveryPort
	}

	// Ensure certificates exist in development mode
	if cfg.Env == "development" && (cfg.TLSCertFile == "" || cfg.TLSKeyFile == "") {
		log.Println("[relay] TLS certificates not specified, generating dev certificates...")
		cert, key, err := config.EnsureDevCertificates(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			log.Fatalf("[relay] Failed to ensure dev certificates: %v", err)
		}
		cfg.TLSCertFile = cert
		cfg.TLSKeyFile = key
	}

	fmt.Println("╔══════════════════════════════════════════════╗")
	fmt.Println("║         MAGNETK P2P RELAY NODE                ║")
	fmt.Println("║         Hybrid MsQuic + libp2p              ║")
	fmt.Println("╚══════════════════════════════════════════════╝")
	fmt.Println()

	// ===== 1. Start libp2p host for discovery =====
	listenAddr := fmt.Sprintf("/ip4/%s/tcp/%d", cfg.EffectiveBindAddress(), cfg.DiscoveryPort)
	log.Printf("[relay] Discovery listen address: %s", listenAddr)

	h, err := libp2p.New(
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Security(noise.ID, noise.New),
	)
	if err != nil {
		log.Fatalf("[relay] Failed to create libp2p host: %v", err)
	}

	log.Printf("[relay] LibP2P Peer ID: %s", h.ID().String())
	for _, addr := range h.Addrs() {
		log.Printf("[relay] Discovery address: %s/p2p/%s", addr, h.ID().String())
	}

	// ===== 2. Start MsQuic transport for data =====
	transportCfg := transport.Config{
		BindAddress:       cfg.EffectiveBindAddress(),
		Port:              uint16(cfg.TransportPort),
		CertFile:          cfg.TLSCertFile,
		KeyFile:           cfg.TLSKeyFile,
		NoCertValidation:  cfg.NoCertValidation,
		ALPN:              "magnetk",
		MaxConnections:    uint32(cfg.MaxConnections),
		IdleTimeoutMs:     uint32(cfg.PingIntervalSec * 3 * 1000),
		SendBufferSize:    cfg.SendBufferSizeBytes(),
		RecvBufferSize:    cfg.RecvBufferSizeBytes(),
		MaxStreamsPerConn: uint16(cfg.MaxStreamsPerConnection),
		MaxThroughput:     true,
	}

	quicTransport, err := transport.NewTransport(transportCfg)
	if err != nil {
		log.Fatalf("[relay] Failed to initialize MsQuic transport: %v", err)
	}
	defer quicTransport.Close()

	log.Printf("[relay] MsQuic transport listening on %s:%d",
		cfg.EffectiveBindAddress(), cfg.TransportPort)

	// ===== 3. Initialize components =====
	relayAddrStr := fmt.Sprintf("%s:%d", cfg.EffectiveBindAddress(), cfg.TransportPort)
	reg := registry.NewFileRegistry(relayAddrStr)
	buf := buffer.NewChunkBuffer(cfg.MaxRelayBufferBytes())

	// ===== 4. Set up handlers =====
	relayHandler := handler.NewRelayHandler(h, reg, buf)
	_ = relayHandler // Stream handlers registered internally

	// Start MsQuic listener for data transfer
	listener, err := quicTransport.Listen(func(stream *transport.Stream, conn *transport.Connection) {
		handleDataStream(stream, conn, reg, buf, cfg)
	})
	if err != nil {
		log.Fatalf("[relay] Failed to start MsQuic listener: %v", err)
	}
	defer listener.Close()

	log.Printf("[relay] MsQuic data listener started on port %d", cfg.TransportPort)

	// ===== 5. Start stats printer =====
	go printStats(reg, h)

	// ===== 6. Wait for shutdown =====
	fmt.Println()
	log.Println("[relay] ✓ Relay node is running. Press Ctrl+C to stop.")
	fmt.Printf("[relay] Environment: %s\n", cfg.Env)
	if cfg.LocalhostMode {
		fmt.Println("[relay] ⚠  Running in LOCALHOST mode (127.0.0.1 only)")
	}
	fmt.Println()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("[relay] Shutting down...")
	h.Close()
	log.Println("[relay] Shutdown complete.")
}

// handleDataStream processes incoming MsQuic streams for data transfer
func handleDataStream(
	stream *transport.Stream,
	conn *transport.Connection,
	reg *registry.FileRegistry,
	buf *buffer.ChunkBuffer,
	cfg *config.Config,
) {
	defer stream.Close()

	log.Printf("[relay] New data stream from connection")

	// TODO: Implement relay data forwarding logic
	// For now, this is a placeholder that reads and forwards data
	// The relay can buffer chunks for offline peers using the buffer package

	_ = reg
	_ = buf
	_ = cfg
}

func printStats(reg *registry.FileRegistry, h host.Host) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		stats := reg.Stats()
		log.Printf("[relay] Stats: %d files registered, %d total seeds, %d total downloads",
			stats.TotalFiles, stats.TotalSeeds, stats.TotalDownloads)
		log.Printf("[relay] Connected peers: %d", len(h.Network().Peers()))
	}
}
