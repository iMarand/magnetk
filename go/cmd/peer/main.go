// Peer client for the MAGNETK P2P network.
//
// The peer downloads files from seeds:
// - Connects to relay via libp2p to discover seeds
// - Downloads chunks from seeds via MsQuic transport
// - Supports download resume via state persistence
// - Verifies file integrity with SHA-256 hashes
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	libp2pProto "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"

	"magnetk/pkg/chunk"
	"magnetk/pkg/config"
	magnetkDHT "magnetk/pkg/dht"
	magnetkLink "magnetk/pkg/magnetk"
	"magnetk/pkg/protocol"
	"magnetk/pkg/state"
	"magnetk/pkg/transport"
	"magnetk/pkg/types"
)

func main() {
	magnetURI := flag.String("magnetk", "", "Magnet link to download")
	relayAddr := flag.String("relay", "", "Relay multiaddr (if not in magnet link)")
	outputPath := flag.String("output", "", "Output file path (default: current directory)")
	configPath := flag.String("config", "./config/settings.conf", "Path to config file")
	enableDHT := flag.Bool("dht", false, "Enable DHT discovery")
	discoveryMode := flag.String("discovery", "auto", "Discovery mode: dht, relay, hybrid, auto")
	bootstrapPeers := flag.String("bootstrap", "", "Comma-separated DHT bootstrap peer multiaddrs")
	holePunch := flag.Bool("holepunch", false, "Enable NAT hole punching")
	relayAssisted := flag.String("relay-assisted", "", "Relay-assisted transfer mode: auto, true, false")
	relayAddresses := flag.String("relay-addresses", "", "Comma-separated additional relay addresses")
	transportMode := flag.String("transport", "", "Transport mode: auto (QUIC+TCP fallback), quic (QUIC only), tcp (TCP only)")
	flag.Parse()

	if *magnetURI == "" {
		fmt.Println("Usage: peer --magnetk <magnet_link> [--output <path>] [--relay <addr>] [--dht]")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  --magnetk    Magnet link (required)")
		fmt.Println("  --output     Output file path (default: filename from magnet)")
		fmt.Println("  --relay      Override relay address")
		fmt.Println("  --dht        Enable DHT discovery")
		fmt.Println("  --discovery  Discovery mode: dht, relay, hybrid, auto")
		fmt.Println("  --bootstrap  Comma-separated DHT bootstrap peer multiaddrs")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Auto-create config if not found
	cfg, err := config.LoadOrCreate(*configPath)
	if err != nil {
		log.Printf("[peer] Warning: Config issue: %v (using defaults)", err)
		cfg = config.DefaultConfig()
	}

	// Apply CLI overrides
	if *enableDHT {
		cfg.DHTEnabled = true
	}
	if *discoveryMode != "auto" {
		cfg.DHTMode = *discoveryMode
	}
	if *bootstrapPeers != "" {
		peers := strings.Split(*bootstrapPeers, ",")
		for i := range peers {
			peers[i] = strings.TrimSpace(peers[i])
		}
		cfg.DHTBootstrapPeers = peers
	}

	// Connection strategy overrides
	if *holePunch {
		cfg.HolePunch = true
	}
	if *relayAssisted != "" {
		cfg.RelayAssisted = *relayAssisted
	}
	if *transportMode != "" {
		cfg.TransportMode = strings.ToLower(*transportMode)
	}
	if *relayAddresses != "" {
		addrs := strings.Split(*relayAddresses, ",")
		for i := range addrs {
			addrs[i] = strings.TrimSpace(addrs[i])
		}
		cfg.RelayAddresses = addrs
	}

	fmt.Println("╔══════════════════════════════════════════════╗")
	fmt.Println("║         MAGNETK P2P PEER CLIENT               ║")
	fmt.Println("║         Hybrid MsQuic + libp2p              ║")
	fmt.Println("╚══════════════════════════════════════════════╝")
	fmt.Println()

	// ===== 1. Parse magnet link =====
	ml, err := magnetkLink.Parse(*magnetURI)
	if err != nil {
		log.Fatalf("[peer] Invalid magnet link: %v", err)
	}

	log.Printf("[peer] File: %s", ml.FileName)
	log.Printf("[peer] Hash: %s", ml.FileHash)
	if ml.FileSize > 0 {
		log.Printf("[peer] Size: %d bytes", ml.FileSize)
	}

	// Determine relay address(es)
	relay := ml.RelayAddr
	if *relayAddr != "" {
		relay = *relayAddr
	}

	// Enable DHT if magnet link has dht=true
	if ml.DHTEnabled {
		cfg.DHTEnabled = true
	}

	// Must have at least one discovery method
	if relay == "" && !cfg.DHTEnabled {
		log.Fatal("[peer] No discovery method available. Use --relay, --dht, or a DHT-enabled magnet link.")
	}

	// Determine output path
	outFile := *outputPath
	if outFile == "" {
		outFile = filepath.Join(".", ml.FileName)
	}

	// ===== 2. Check for existing transfer state =====
	stateMgr, err := state.NewManager(cfg.StateDir, cfg.StateExpiryDays)
	if err != nil {
		log.Fatalf("[peer] Failed to initialize state manager: %v", err)
	}
	existingState, _ := stateMgr.FindByFileHash(ml.FileHash)
	if existingState != nil {
		log.Printf("[peer] Found existing transfer state: %d/%d chunks complete",
			len(existingState.CompletedChunks), existingState.TotalChunks)
	}

	// ===== 3. Start libp2p for discovery + relay/hole-punch fallback =====
	listenAddrs := []string{
		fmt.Sprintf("/ip4/%s/tcp/0", cfg.EffectiveBindAddress()),
		fmt.Sprintf("/ip4/%s/udp/0/quic-v1", cfg.EffectiveBindAddress()),
	}
	p2pOpts := []libp2p.Option{
		libp2p.ListenAddrStrings(listenAddrs...),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Security(noise.ID, noise.New),
	}
	if cfg.HolePunch {
		p2pOpts = append(p2pOpts,
			libp2p.NATPortMap(),
			libp2p.EnableHolePunching(),
		)
		log.Println("[peer] libp2p QUIC + DCUtR hole punching enabled")
	}

	h, err := libp2p.New(p2pOpts...)
	if err != nil {
		log.Fatalf("[peer] Failed to create libp2p host: %v", err)
	}
	defer h.Close()

	log.Printf("[peer] Peer ID: %s", h.ID().String())

	// ===== 4. Initialize DHT (if enabled) =====
	var dhtNode *magnetkDHT.DHT
	if cfg.DHTEnabled {
		log.Println("[peer] Initializing DHT for discovery...")
		dhtCfg := &magnetkDHT.Config{
			Mode:           "client",
			BootstrapPeers: cfg.DHTBootstrapPeers,
		}
		dhtNode, err = magnetkDHT.New(h, dhtCfg)
		if err != nil {
			log.Printf("[peer] Warning: DHT init failed: %v", err)
		} else {
			defer dhtNode.Close()
			if err := dhtNode.Bootstrap(); err != nil {
				log.Printf("[peer] Warning: DHT bootstrap partially failed: %v", err)
			}
		}
	}

	// ===== 5. Discover seeds (fallback chain) =====
	seeds, err := discoverSeeds(h, dhtNode, relay, ml.FileHash, cfg)
	if err != nil || len(seeds) == 0 {
		log.Fatalf("[peer] No seeds found for file %s", ml.FileHash[:12])
	}
	log.Printf("[peer] Found %d seed(s)", len(seeds))

	// ===== 6. Download with fallback through all seeds =====
	// Filter non-routable addresses from seeds
	var routableSeeds []protocol.SeedInfo
	for _, s := range seeds {
		if s.TransportAddr == "" {
			continue
		}
		host, _ := parseHostPort(s.TransportAddr)
		if isRoutableAddr(host) {
			routableSeeds = append(routableSeeds, s)
		} else {
			log.Printf("[peer] Skipping non-routable seed address: %s", s.TransportAddr)
		}
	}
	log.Printf("[peer] Routable seeds to try: %d", len(routableSeeds))

	// Set up download with cancel support
	doneCh := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		defer close(doneCh)
		for i, s := range routableSeeds {
			log.Printf("[peer] Trying seed %d/%d: %s at %s", i+1, len(routableSeeds), s.PeerID[:12], s.TransportAddr)
			err := downloadFile(s, ml, outFile, cfg, stateMgr, existingState)
			if err == nil {
				return // Success!
			}
			log.Printf("[peer] Seed %s failed: %v", s.PeerID[:12], err)
		}
		log.Printf("[peer] All %d direct seed attempts failed", len(routableSeeds))

		// ===== libp2p tunnel fallback =====
		// All direct addresses unreachable (typically: both peers behind
		// NATs without UPnP). Connect by libp2p PeerID through the relay's
		// circuit-relay-v2 service. Slower than direct (data flows via the
		// relay) but works across any NAT topology.
		if relay == "" {
			return
		}
		log.Println("[peer] Trying libp2p circuit-relay tunnel...")
		for _, s := range seeds {
			if s.PeerID == "" {
				continue
			}
			err := downloadFileViaLibp2p(h, s, relay, ml, outFile, cfg, stateMgr, existingState)
			if err == nil {
				return
			}
			log.Printf("[peer] libp2p tunnel to %s failed: %v", s.PeerID[:12], err)
		}
		log.Println("[peer] All discovery and connection methods exhausted")
	}()

	select {
	case <-doneCh:
		// Download completed
	case <-sigCh:
		log.Println("[peer] Download interrupted. State saved for resume.")
	}
}

// downloadFile downloads a file from a seed via MsQuic (preferred) or TCP fallback
func downloadFile(
	seed protocol.SeedInfo,
	ml *magnetkLink.MagnetLink,
	outFile string,
	cfg *config.Config,
	stateMgr *state.Manager,
	existingState *types.TransferState,
) error {
	host, port := parseHostPort(seed.TransportAddr)
	var reader *protocol.FrameReader
	var writer *protocol.FrameWriter
	var cleanup func()

	// Try MsQuic first, fall back to TCP (respects TransportMode config)
	msquicOK := false
	mode := cfg.TransportMode
	if mode == "" {
		mode = "auto"
	}
	log.Printf("[peer] Connecting to seed %s at %s (transport: %s)", seed.PeerID[:12], seed.TransportAddr, mode)

	// --- Attempt 1: MsQuic (skip if mode == "tcp") ---
	if mode != "tcp" {
		transportCfg := transport.Config{
			BindAddress:       cfg.EffectiveBindAddress(),
			Port:              0,
			NoCertValidation:  cfg.NoCertValidation,
			ALPN:              "magnetk",
			MaxConnections:    10,
			IdleTimeoutMs:     60000,
			SendBufferSize:    cfg.SendBufferSizeBytes(),
			RecvBufferSize:    cfg.RecvBufferSizeBytes(),
			MaxStreamsPerConn: uint16(cfg.MaxStreamsPerConnection),
			MaxThroughput:     true,
		}

		quicTransport, err := transport.NewTransport(transportCfg)
		if err == nil {
			conn, err := quicTransport.Connect(host, port)
			if err == nil {
				stream, err := conn.OpenStream()
				if err == nil {
					log.Println("[peer] Connected via MsQuic")
					reader = protocol.NewFrameReader(stream)
					writer = protocol.NewFrameWriter(stream)
					cleanup = func() {
						stream.Close()
						conn.Close()
						quicTransport.Close()
					}
					msquicOK = true
				} else {
					conn.Close()
					quicTransport.Close()
				}
			} else {
				quicTransport.Close()
			}
		}
		if !msquicOK {
			log.Printf("[peer] MsQuic unavailable, trying TCP fallback to %s:%d", host, port)
		}
	}

	// --- Attempt 2: TCP fallback (skip if mode == "quic") ---
	if !msquicOK && mode != "quic" {
		tcpConn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 10*time.Second)
		if err != nil {
			return fmt.Errorf("connect to seed (TCP fallback): %w", err)
		}
		log.Println("[peer] Connected via TCP fallback")
		reader = protocol.NewFrameReader(tcpConn)
		writer = protocol.NewFrameWriter(tcpConn)
		cleanup = func() { tcpConn.Close() }
	}

	defer cleanup()
	return runChunkProtocol(reader, writer, ml, outFile, cfg, stateMgr, existingState)
}

// downloadFileViaLibp2p connects to the seed by libp2p PeerID through the
// relay's circuit-relay-v2 service, then runs the chunk protocol over the
// libp2p stream. Used as a last resort when no direct address worked —
// circuit relay traverses any NAT but routes data through the relay host.
func downloadFileViaLibp2p(
	h host.Host,
	seed protocol.SeedInfo,
	relayAddrStr string,
	ml *magnetkLink.MagnetLink,
	outFile string,
	cfg *config.Config,
	stateMgr *state.Manager,
	existingState *types.TransferState,
) error {
	if relayAddrStr == "" {
		return fmt.Errorf("no relay address for libp2p tunnel")
	}
	relayMA, err := multiaddr.NewMultiaddr(relayAddrStr)
	if err != nil {
		return fmt.Errorf("invalid relay multiaddr: %w", err)
	}
	relayInfo, err := peer.AddrInfoFromP2pAddr(relayMA)
	if err != nil {
		return fmt.Errorf("parse relay peer info: %w", err)
	}

	connectCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.Connect(connectCtx, *relayInfo); err != nil {
		return fmt.Errorf("connect to relay: %w", err)
	}

	seedID, err := peer.Decode(seed.PeerID)
	if err != nil {
		return fmt.Errorf("decode seed peer id: %w", err)
	}

	// Add a circuit-relay address for the seed: dialing this multiaddr
	// makes libp2p route through the relay's circuit service to reach the
	// seed even when the seed has no public address.
	circuitAddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("%s/p2p-circuit", relayAddrStr))
	if err != nil {
		return fmt.Errorf("build circuit multiaddr: %w", err)
	}
	h.Peerstore().AddAddr(seedID, circuitAddr, peerstore.TempAddrTTL)

	// Open the magnetk transfer stream. WithAllowLimitedConn lets libp2p
	// use the relayed (resource-limited) connection for this stream.
	streamCtx, streamCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer streamCancel()
	streamCtx = network.WithAllowLimitedConn(streamCtx, "magnetk")

	stream, err := h.NewStream(streamCtx, seedID, libp2pProto.ID(protocol.MagnetProtocolID))
	if err != nil {
		return fmt.Errorf("open libp2p stream to seed: %w", err)
	}

	if stream.Conn().Stat().Limited {
		log.Printf("[peer] Connected via libp2p circuit relay to seed %s", seed.PeerID[:12])
		if directStream, ok := tryHolePunchedLibp2pStream(h, seedID, seed.PeerID[:12]); ok {
			stream.Close()
			stream = directStream
		} else if cfg.RelayAssisted == "false" {
			stream.Close()
			return fmt.Errorf("direct libp2p hole punch unavailable and relay-assisted=false")
		}
	} else {
		log.Printf("[peer] Connected via direct libp2p stream to seed %s", seed.PeerID[:12])
	}
	defer stream.Close()

	reader := protocol.NewFrameReader(stream)
	writer := protocol.NewFrameWriter(stream)
	return runChunkProtocol(reader, writer, ml, outFile, cfg, stateMgr, existingState)
}

func tryHolePunchedLibp2pStream(h host.Host, seedID peer.ID, shortID string) (network.Stream, bool) {
	log.Printf("[peer] Waiting briefly for libp2p DCUtR hole-punched direct path to %s...", shortID)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		for _, conn := range h.Network().ConnsToPeer(seedID) {
			if !conn.Stat().Limited {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				stream, err := h.NewStream(ctx, seedID, libp2pProto.ID(protocol.MagnetProtocolID))
				cancel()
				if err == nil && !stream.Conn().Stat().Limited {
					log.Printf("[peer] Upgraded to direct libp2p hole-punched path for seed %s", shortID)
					return stream, true
				}
				if err == nil {
					stream.Close()
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	log.Printf("[peer] No direct libp2p hole-punched path available; continuing through relay")
	return nil, false
}

// runChunkProtocol drives manifest + chunk requests over an already-open
// connection. Used by both direct (MsQuic/TCP) and libp2p-tunnel paths.
func runChunkProtocol(
	reader *protocol.FrameReader,
	writer *protocol.FrameWriter,
	ml *magnetkLink.MagnetLink,
	outFile string,
	cfg *config.Config,
	stateMgr *state.Manager,
	existingState *types.TransferState,
) error {
	// ===== Request manifest =====
	log.Println("[peer] Requesting manifest...")
	err := writer.WriteJSON(protocol.MsgManifestReq, protocol.ManifestRequestPayload{
		FileHash: ml.FileHash,
	})
	if err != nil {
		return fmt.Errorf("send manifest request: %w", err)
	}

	frame, err := reader.ReadFrame()
	if err != nil {
		return fmt.Errorf("read manifest response: %w", err)
	}
	if frame.Type != protocol.MsgManifestRes {
		return fmt.Errorf("unexpected response type: 0x%02x", frame.Type)
	}

	var manifest protocol.ManifestResponsePayload
	if err := json.Unmarshal(frame.Payload, &manifest); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}

	log.Printf("[peer] File: %s (%d bytes, %d chunks)",
		manifest.FileName, manifest.FileSize, manifest.TotalChunks)

	// ===== Determine chunks to download =====
	completedMap := make(map[uint32]bool)
	if existingState != nil {
		for _, idx := range existingState.CompletedChunks {
			completedMap[idx] = true
		}
	}
	var completedBytes int64
	for _, meta := range manifest.Chunks {
		if completedMap[meta.Index] {
			completedBytes += meta.Size
		}
	}

	var neededChunks []uint32
	for i := uint32(0); i < manifest.TotalChunks; i++ {
		if !completedMap[i] {
			neededChunks = append(neededChunks, i)
		}
	}

	if len(neededChunks) == 0 {
		log.Println("[peer] All chunks already downloaded!")
		return nil
	}

	log.Printf("[peer] Need %d chunks (already have %d)",
		len(neededChunks), len(completedMap))

	// ===== Create/open output file =====
	file, err := os.OpenFile(outFile, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open output file: %w", err)
	}
	defer file.Close()

	// Pre-allocate file
	if manifest.FileSize > 0 {
		file.Truncate(manifest.FileSize)
	}

	// Initialize transfer state
	transferState := &types.TransferState{
		TransferID:      fmt.Sprintf("dl-%s-%d", ml.FileHash[:8], time.Now().Unix()),
		FileHash:        ml.FileHash,
		FileName:        manifest.FileName,
		FileSize:        manifest.FileSize,
		TotalChunks:     manifest.TotalChunks,
		CompletedChunks: make([]uint32, 0),
		OutputPath:      outFile,
	}
	if existingState != nil {
		transferState = existingState
	}

	// ===== Request chunks in batches =====
	batchSize := cfg.MaxParallelChunks
	startTime := time.Now()
	downloaded := 0
	downloadedBytes := completedBytes

	for i := 0; i < len(neededChunks); i += batchSize {
		end := i + batchSize
		if end > len(neededChunks) {
			end = len(neededChunks)
		}
		batch := neededChunks[i:end]

		// Send chunk request
		err = writer.WriteJSON(protocol.MsgChunkReq, protocol.ChunkRequestPayload{
			FileHash:     ml.FileHash,
			ChunkIndices: batch,
		})
		if err != nil {
			return fmt.Errorf("send chunk request: %w", err)
		}

		// Receive chunks
		for range batch {
			nextChunkOrdinal := downloaded + len(completedMap) + 1
			lastProgressPrint := time.Now().Add(-time.Second)
			frame, err := reader.ReadFrameWithProgress(func(read, total uint32) {
				if total < 64*1024 || time.Since(lastProgressPrint) < 250*time.Millisecond {
					return
				}
				lastProgressPrint = time.Now()
				elapsed := time.Since(startTime).Seconds()
				receivedBytes := downloadedBytes + int64(read)
				speed := float64(receivedBytes-completedBytes) / elapsed / 1024 / 1024
				fmt.Printf("\r[peer] Receiving chunk %d/%d: %.2f/%.2f MB | total %.2f/%.2f MB | %.2f MB/s",
					nextChunkOrdinal,
					manifest.TotalChunks,
					float64(read)/1024/1024,
					float64(total)/1024/1024,
					float64(receivedBytes)/1024/1024,
					float64(manifest.FileSize)/1024/1024,
					speed)
			})
			if err != nil {
				if err == io.EOF {
					return fmt.Errorf("connection closed before all chunks received")
				}
				return fmt.Errorf("read chunk: %w", err)
			}

			if frame.Type == protocol.MsgError {
				var errPayload protocol.ErrorPayload
				json.Unmarshal(frame.Payload, &errPayload)
				return fmt.Errorf("server error: %s", errPayload.Message)
			}

			if frame.Type != protocol.MsgChunkRes {
				return fmt.Errorf("unexpected message type: 0x%02x", frame.Type)
			}

			// Parse binary chunk data
			chunkIdx, chunkData, err := protocol.ParseChunkPayload(frame.Payload)
			if err != nil {
				return fmt.Errorf("parse chunk %d: %w", chunkIdx, err)
			}

			// Write chunk to file at correct offset
			offset := int64(chunkIdx) * manifest.ChunkSize
			if _, err := file.WriteAt(chunkData, offset); err != nil {
				return fmt.Errorf("write chunk %d: %w", chunkIdx, err)
			}

			downloaded++
			downloadedBytes += int64(len(chunkData))
			transferState.CompletedChunks = append(transferState.CompletedChunks, chunkIdx)

			// Progress
			pct := float64(downloaded+len(completedMap)) / float64(manifest.TotalChunks) * 100
			elapsed := time.Since(startTime).Seconds()
			speed := float64(downloadedBytes-completedBytes) / elapsed / 1024 / 1024

			fmt.Printf("\r[peer] Progress: %.1f%% (%d/%d chunks, %.2f/%.2f MB) | %.2f MB/s",
				pct,
				downloaded+len(completedMap),
				manifest.TotalChunks,
				float64(downloadedBytes)/1024/1024,
				float64(manifest.FileSize)/1024/1024,
				speed)

			// Save state periodically
			if downloaded%10 == 0 {
				stateMgr.Save(transferState)
			}
		}
	}
	fmt.Println()

	// Save final state
	stateMgr.Save(transferState)

	// ===== Verify file =====
	log.Println("[peer] Verifying file integrity...")
	file.Close()
	valid, err := chunk.VerifyFile(outFile, ml.FileHash)
	if err != nil {
		return fmt.Errorf("verify file: %w", err)
	}

	if valid {
		fmt.Println()
		fmt.Println("╔══════════════════════════════════════════════╗")
		fmt.Println("║  DOWNLOAD COMPLETE! ✓ File verified.        ║")
		fmt.Println("╚══════════════════════════════════════════════╝")
		fmt.Printf("  Output: %s\n", outFile)
		fmt.Printf("  Hash:   %s\n", ml.FileHash)

		// Clean up state
		stateMgr.Delete(transferState.TransferID)
	} else {
		fmt.Println()
		log.Println("[peer] ✗ File verification FAILED! Hash mismatch.")
		return fmt.Errorf("file hash mismatch")
	}

	return nil
}

// discoverSeeds tries to find seeds using the configured discovery priority.
// Fallback chain: DHT → Relay → Direct
func discoverSeeds(h interface{}, dhtNode *magnetkDHT.DHT, relayAddrStr string, fileHash string, cfg *config.Config) ([]protocol.SeedInfo, error) {
	var seeds []protocol.SeedInfo
	var lastErr error

	// Determine discovery order
	priority := cfg.DiscoveryPriority
	if len(priority) == 0 {
		// Auto: try DHT first if enabled, then relay
		if cfg.DHTEnabled {
			priority = []string{"dht", "relay", "direct"}
		} else {
			priority = []string{"relay", "direct"}
		}
	}

	for _, method := range priority {
		switch method {
		case "dht":
			if dhtNode == nil {
				continue
			}
			log.Println("[peer] Trying DHT discovery...")
			providers, err := dhtNode.FindProviders(fileHash, 30*time.Second)
			if err != nil {
				log.Printf("[peer] DHT discovery failed: %v", err)
				lastErr = err
				continue
			}

			// Convert DHT providers to SeedInfo
			for _, pi := range providers {
				// Extract best transport address from the provider's multiaddrs
				var transportAddr string
				var addrs []string
				for _, addr := range pi.Addrs {
					addrStr := addr.String()
					addrs = append(addrs, addrStr)
					// Extract IP from multiaddr for transport connection
					parts := strings.Split(addrStr, "/")
					if len(parts) >= 3 && parts[1] == "ip4" {
						ip := parts[2]
						// Use configured transport port (since DHT only gives discovery addresses)
						transportAddr = fmt.Sprintf("%s:%d", ip, cfg.TransportPort)
					}
				}

				if transportAddr != "" {
					seeds = append(seeds, protocol.SeedInfo{
						PeerID:        pi.ID.String(),
						TransportAddr: transportAddr,
						Addresses:     addrs,
					})
				}
			}

			if len(seeds) > 0 {
				log.Printf("[peer] DHT discovered %d seed(s)", len(seeds))
				return seeds, nil
			}

		case "relay":
			if relayAddrStr == "" {
				continue
			}
			log.Println("[peer] Trying relay discovery...")

			// Try to connect to relay and lookup seeds
			relaySeeds, err := lookupSeedsViaRelay(h, relayAddrStr, fileHash)
			if err != nil {
				log.Printf("[peer] Relay discovery failed: %v", err)
				lastErr = err

				// Try multi-relay failover
				if len(cfg.RelayAddresses) > 0 {
					for _, altRelay := range cfg.RelayAddresses {
						if altRelay == relayAddrStr {
							continue // Skip the one we already tried
						}
						log.Printf("[peer] Trying alternate relay: %s", altRelay)
						relaySeeds, err = lookupSeedsViaRelay(h, altRelay, fileHash)
						if err == nil && len(relaySeeds) > 0 {
							break
						}
					}
				}
			}

			if len(relaySeeds) > 0 {
				log.Printf("[peer] Relay discovered %d seed(s)", len(relaySeeds))
				return relaySeeds, nil
			}

		case "direct":
			// Try direct connection using seed ID from magnet link
			log.Println("[peer] Trying direct connection...")
			// Direct connection requires knowing the seed's transport address
			// which typically comes from DHT or relay. This is a last resort.
			log.Println("[peer] Direct discovery: no known seed addresses")
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("all discovery methods failed, last error: %w", lastErr)
	}
	return nil, fmt.Errorf("no seeds found via any discovery method")
}

// lookupSeedsViaRelay connects to the relay's TCP discovery port (libp2p
// port + 2 by convention, matching cmd/relay/main.go) and queries for
// seeds of a file using the binary framing protocol — same path the JS
// and Python SDKs use, so seeds published from any SDK are found here.
func lookupSeedsViaRelay(_ interface{}, relayAddrStr string, fileHash string) ([]protocol.SeedInfo, error) {
	relayHost, libp2pPort, err := splitRelayMultiaddr(relayAddrStr)
	if err != nil {
		return nil, err
	}
	discoveryPort := libp2pPort + 2

	log.Printf("[peer] Looking up seeds for %s on %s:%d...", fileHash[:12], relayHost, discoveryPort)

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", relayHost, discoveryPort), 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial relay discovery: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(15 * time.Second))

	writer := protocol.NewFrameWriter(conn)
	reader := protocol.NewFrameReader(conn)

	if err := writer.WriteJSON(protocol.MsgLookupPeer, protocol.LookupPeerPayload{FileHash: fileHash}); err != nil {
		return nil, fmt.Errorf("send lookup request: %w", err)
	}

	frame, err := reader.ReadFrame()
	if err != nil {
		return nil, fmt.Errorf("read lookup response: %w", err)
	}
	if frame.Type == protocol.MsgError {
		var ep protocol.ErrorPayload
		json.Unmarshal(frame.Payload, &ep)
		return nil, fmt.Errorf("relay error: %s", ep.Message)
	}
	if frame.Type != protocol.MsgLookupResult {
		return nil, fmt.Errorf("unexpected response type 0x%02x", frame.Type)
	}

	var result protocol.LookupResultPayload
	if err := json.Unmarshal(frame.Payload, &result); err != nil {
		return nil, fmt.Errorf("parse lookup result: %w", err)
	}
	if !result.Found {
		return nil, nil
	}
	return result.Seeds, nil
}

// splitRelayMultiaddr extracts host and libp2p TCP port from a multiaddr
// like /ip4/HOST/tcp/PORT/p2p/PEERID.
func splitRelayMultiaddr(addr string) (string, int, error) {
	parts := strings.Split(addr, "/")
	if len(parts) < 5 || parts[1] != "ip4" || parts[3] != "tcp" {
		return "", 0, fmt.Errorf("unsupported relay multiaddr: %s", addr)
	}
	port, err := strconv.Atoi(parts[4])
	if err != nil {
		return "", 0, fmt.Errorf("invalid port in relay multiaddr: %w", err)
	}
	return parts[2], port, nil
}

func parseHostPort(addr string) (string, uint16) {
	// Simple host:port parser
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			host := addr[:i]
			var port uint16
			fmt.Sscanf(addr[i+1:], "%d", &port)
			return host, port
		}
	}
	return addr, 4002
}

// isRoutableAddr returns true if host is dialable from this machine.
// Accepts LAN/RFC1918 ranges (peers on the same network reach each other
// directly via LAN IP — no NAT involved). Rejects only the bind-all
// sentinel, link-local, and loopback (which is added separately).
func isRoutableAddr(host string) bool {
	if host == "" || host == "0.0.0.0" {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	return true
}
