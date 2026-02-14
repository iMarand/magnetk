// Seed node for the Magnetk P2P network.
//
// The seed hosts files and serves chunks to requesting peers:
// - Connects to relay via libp2p for discovery/registration
// - Opens MsQuic transport for serving file chunks
// - Generates file manifests and publishes to relay
// - Serves chunks using binary framing protocol
package main

import (
	"bufio"
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
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"

	"magnetk/pkg/chunk"
	"magnetk/pkg/config"
	magnetkDHT "magnetk/pkg/dht"
	magnetkLink "magnetk/pkg/magnetk"
	"magnetk/pkg/protocol"
	"magnetk/pkg/transport"
)

func main() {
	relayAddr := flag.String("relay", "", "Relay multiaddr (optional when --dht is enabled)")
	filePath := flag.String("file", "", "File to seed (required)")
	configPath := flag.String("config", "./config/settings.conf", "Path to config file")
	portStr := flag.Int("port", 0, "Transport port (overrides config)")
	enableDHT := flag.Bool("dht", false, "Enable DHT for decentralized discovery")
	discoveryMode := flag.String("discovery", "auto", "Discovery mode: dht, relay, hybrid, auto")
	bootstrapPeers := flag.String("bootstrap", "", "Comma-separated DHT bootstrap peer multiaddrs")
	flag.Parse()

	if *filePath == "" {
		fmt.Println("Usage: seed --file <file_path> [--relay <relay_multiaddr>] [--dht]")
		fmt.Println("  --file       Path to the file to share (required)")
		fmt.Println("  --relay      Relay node multiaddr (optional with --dht)")
		fmt.Println("  --dht        Enable DHT for decentralized discovery")
		fmt.Println("  --discovery  Discovery mode: dht, relay, hybrid, auto (default: auto)")
		fmt.Println("  --bootstrap  Comma-separated DHT bootstrap peer multiaddrs")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Require at least one discovery method
	if *relayAddr == "" && !*enableDHT {
		fmt.Println("Error: Must specify --relay or --dht (or both for hybrid mode)")
		os.Exit(1)
	}

	// Auto-create config if not found
	cfg, err := config.LoadOrCreate(*configPath)
	if err != nil {
		log.Printf("[seed] Warning: Config issue: %v (using defaults)", err)
		cfg = config.DefaultConfig()
	}

	// Apply CLI overrides to config
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

	if *portStr != 0 {
		cfg.TransportPort = *portStr
	}

	// Always ensure certificates exist (Linux MsQuic requires them)
	if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
		log.Println("[seed] TLS certificates not specified, generating dev certificates...")
		cert, key, err := config.EnsureDevCertificates(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			log.Fatalf("[seed] Failed to ensure dev certificates: %v", err)
		}
		cfg.TLSCertFile = cert
		cfg.TLSKeyFile = key
	}

	fmt.Println("╔══════════════════════════════════════════════╗")
	fmt.Println("║         MAGNETK P2P SEED NODE                 ║")
	fmt.Println("║         Hybrid MsQuic + libp2p              ║")
	fmt.Println("╚══════════════════════════════════════════════╝")
	fmt.Println()

	// Verify file exists
	fileInfo, err := os.Stat(*filePath)
	if err != nil {
		log.Fatalf("[seed] File not found: %v", err)
	}
	log.Printf("[seed] File: %s (%d bytes)", *filePath, fileInfo.Size())

	// ===== 1. Generate file manifest =====
	chunkMgr := chunk.NewManager(cfg.ChunkSizeBytes())
	manifest, err := chunkMgr.GenerateManifest(*filePath)
	if err != nil {
		log.Fatalf("[seed] Failed to generate manifest: %v", err)
	}
	log.Printf("[seed] Manifest: hash=%s, chunks=%d, chunk_size=%d",
		manifest.FileHash, manifest.TotalChunks, manifest.ChunkSize)

	// ===== 2. Start libp2p host for discovery =====
	listenAddr := fmt.Sprintf("/ip4/%s/tcp/0", cfg.EffectiveBindAddress())
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Security(noise.ID, noise.New),
		libp2p.NATPortMap(), // Use UPnP/PMP for automatic port forwarding
	)
	if err != nil {
		log.Fatalf("[seed] Failed to create libp2p host: %v", err)
	}
	defer h.Close()

	log.Printf("[seed] LibP2P Peer ID: %s", h.ID().String())

	// ===== 3. Start MsQuic transport for data =====
	transportCfg := transport.Config{
		BindAddress:       cfg.EffectiveBindAddress(),
		Port:              uint16(cfg.TransportPort),
		CertFile:          cfg.TLSCertFile,
		KeyFile:           cfg.TLSKeyFile,
		NoCertValidation:  cfg.NoCertValidation,
		ALPN:              "magnetk",
		MaxConnections:    uint32(cfg.MaxConnections),
		IdleTimeoutMs:     60000,
		SendBufferSize:    cfg.SendBufferSizeBytes(),
		RecvBufferSize:    cfg.RecvBufferSizeBytes(),
		MaxStreamsPerConn: uint16(cfg.MaxStreamsPerConnection),
		MaxThroughput:     true,
	}

	quicTransport, err := transport.NewTransport(transportCfg)
	if err != nil {
		log.Fatalf("[seed] Failed to init MsQuic transport: %v", err)
	}
	defer quicTransport.Close()

	// Start MsQuic listener for chunk requests (Primary Transport)
	listener, err := quicTransport.Listen(func(stream *transport.Stream, conn *transport.Connection) {
		handleChunkRequest(stream, conn, manifest, *filePath, chunkMgr)
	})
	if err != nil {
		log.Fatalf("[seed] Failed to start MsQuic listener: %v", err)
	}
	defer listener.Close()

	log.Printf("[seed] MsQuic data transport on port %d", cfg.TransportPort)

	// Add TCP Listener for JS SDK / Legacy clients (Fallback Transport)
	// This allows pure Node.js clients to connect without C++ bindings
	go func() {
		tcpAddr := fmt.Sprintf("%s:%d", cfg.EffectiveBindAddress(), cfg.TransportPort)
		tcpListener, err := net.Listen("tcp", tcpAddr)
		if err != nil {
			log.Printf("[seed] Warning: Failed to start TCP listener (JS SDK might not work): %v", err)
			return
		}
		log.Printf("[seed] TCP fallback transport listening on %s", tcpAddr)

		for {
			conn, err := tcpListener.Accept()
			if err != nil {
				log.Printf("[seed] TCP accept error: %v", err)
				continue
			}
			go handleTCPChunkRequest(conn, manifest, *filePath, chunkMgr)
		}
	}()

	// ===== 4. Initialize DHT (if enabled) =====
	var dhtNode *magnetkDHT.DHT
	if cfg.DHTEnabled {
		log.Println("[seed] Initializing DHT for decentralized discovery...")
		dhtCfg := &magnetkDHT.Config{
			Mode:               "server",
			BootstrapPeers:     cfg.DHTBootstrapPeers,
			ReannounceInterval: time.Duration(cfg.DHTReannounceIntervalMin) * time.Minute,
		}
		dhtNode, err = magnetkDHT.New(h, dhtCfg)
		if err != nil {
			log.Fatalf("[seed] Failed to create DHT: %v", err)
		}
		defer dhtNode.Close()

		if err := dhtNode.Bootstrap(); err != nil {
			log.Printf("[seed] Warning: DHT bootstrap partially failed: %v", err)
		}

		// Announce file to DHT
		if err := dhtNode.Announce(manifest.FileHash); err != nil {
			log.Printf("[seed] Warning: DHT announce failed: %v", err)
		} else {
			log.Printf("[seed] File announced to DHT: %s", manifest.FileHash[:12])
		}
	}

	// ===== 5. Connect to relay and publish file (if relay provided) =====
	var relayID peer.ID
	if *relayAddr != "" {
		relayMA, err := multiaddr.NewMultiaddr(*relayAddr)
		if err != nil {
			log.Fatalf("[seed] Invalid relay address: %v", err)
		}

		relayInfo, err := peer.AddrInfoFromP2pAddr(relayMA)
		if err != nil {
			log.Fatalf("[seed] Failed to parse relay peer info: %v", err)
		}

		if err := h.Connect(context.Background(), *relayInfo); err != nil {
			log.Fatalf("[seed] Failed to connect to relay: %v", err)
		}
		log.Println("[seed] Connected to relay")

		// Register with relay
		err = publishToRelay(h, relayInfo.ID, manifest, *filePath, cfg)
		if err != nil {
			log.Fatalf("[seed] Failed to publish file: %v", err)
		}
		relayID = relayInfo.ID
	} else {
		log.Println("[seed] No relay specified - using DHT-only discovery")
	}

	// Generate magnet link
	ml := &magnetkLink.MagnetLink{
		FileHash:   manifest.FileHash,
		FileName:   filepath.Base(*filePath),
		FileSize:   manifest.FileSize,
		SeedID:     h.ID().String(),
		RelayAddr:  *relayAddr,
		DHTEnabled: cfg.DHTEnabled,
	}

	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════╗")
	fmt.Println("║  FILE PUBLISHED SUCCESSFULLY!                ║")
	fmt.Println("╚══════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("  Magnetk Link: %s\n\n", ml.String())
	if cfg.DHTEnabled {
		fmt.Println("  Discovery: DHT (decentralized)")
	}
	if *relayAddr != "" {
		fmt.Println("  Discovery: Relay (centralized)")
	}
	fmt.Println("  Share this link to allow others to download!")
	fmt.Println()

	// ===== 6. Heartbeat loop (relay only) =====
	if *relayAddr != "" {
		go func() {
			ticker := time.NewTicker(time.Duration(cfg.PingIntervalSec) * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if err := sendHeartbeat(h, relayID); err != nil {
					log.Printf("[seed] Heartbeat failed: %v", err)
				}
			}
		}()
	}

	// ===== 7. Wait for shutdown =====
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("[seed] Shutting down...")
}

// handleChunkRequest handles incoming chunk requests via MsQuic
func handleChunkRequest(
	stream *transport.Stream,
	conn *transport.Connection,
	manifest *chunk.FileManifest,
	filePath string,
	chunkMgr *chunk.Manager,
) {
	defer stream.Close()
	reader := protocol.NewFrameReader(stream)
	writer := protocol.NewFrameWriter(stream)
	serveChunkRequest(reader, writer, manifest, filePath, chunkMgr, "MsQuic")
}

// handleTCPChunkRequest handles incoming chunk requests via TCP (fallback)
func handleTCPChunkRequest(
	conn net.Conn,
	manifest *chunk.FileManifest,
	filePath string,
	chunkMgr *chunk.Manager,
) {
	defer conn.Close()
	reader := protocol.NewFrameReader(conn)
	writer := protocol.NewFrameWriter(conn)
	serveChunkRequest(reader, writer, manifest, filePath, chunkMgr, "TCP")
}

// serveChunkRequest processes protocol messages for a connection
func serveChunkRequest(
	reader *protocol.FrameReader,
	writer *protocol.FrameWriter,
	manifest *chunk.FileManifest,
	filePath string,
	chunkMgr *chunk.Manager,
	transportType string,
) {
	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			if err == io.EOF {
				return
			}
			log.Printf("[seed] Error reading frame (%s): %v", transportType, err)
			return
		}

		switch frame.Type {
		case protocol.MsgManifestReq:
			// Parse request
			var req protocol.ManifestRequestPayload
			if err := json.Unmarshal(frame.Payload, &req); err != nil {
				log.Printf("[seed] Invalid manifest request: %v", err)
				continue
			}

			// Build response
			resp := protocol.ManifestResponsePayload{
				FileHash:    manifest.FileHash,
				FileName:    manifest.FileName,
				FileSize:    manifest.FileSize,
				ChunkSize:   int64(manifest.ChunkSize),
				TotalChunks: manifest.TotalChunks,
				Chunks:      make([]protocol.ChunkMeta, len(manifest.Chunks)),
			}
			for i, c := range manifest.Chunks {
				resp.Chunks[i] = protocol.ChunkMeta{
					Index:  c.Index,
					Hash:   c.Hash,
					Offset: c.Offset,
					Size:   int64(c.Size),
				}
			}

			if err := writer.WriteJSON(protocol.MsgManifestRes, resp); err != nil {
				log.Printf("[seed] Error sending manifest (%s): %v", transportType, err)
				return
			}
			log.Printf("[seed] Sent manifest for %s via %s", req.FileHash[:12], transportType)

		case protocol.MsgChunkReq:
			// Parse request
			var req protocol.ChunkRequestPayload
			if err := json.Unmarshal(frame.Payload, &req); err != nil {
				log.Printf("[seed] Invalid chunk request: %v", err)
				continue
			}

			// Serve requested chunks
			for _, idx := range req.ChunkIndices {
				chunkData, err := chunkMgr.ReadChunk(filePath, idx)
				if err != nil {
					log.Printf("[seed] Error reading chunk %d: %v", idx, err)
					errPayload := protocol.ErrorPayload{
						Code:    500,
						Message: fmt.Sprintf("chunk read error: %v", err),
					}
					writer.WriteJSON(protocol.MsgError, errPayload)
					continue
				}

				// Send chunk as raw binary (no JSON, no base64)
				if err := writer.WriteChunk(idx, chunkData.Data); err != nil {
					log.Printf("[seed] Error sending chunk %d (%s): %v", idx, transportType, err)
					return
				}
			}

		default:
			log.Printf("[seed] Unknown message type: 0x%02x", frame.Type)
		}
	}
}

// publishToRelay registers the file with the relay via libp2p discovery stream
func publishToRelay(h host.Host, relayID peer.ID, manifest *chunk.FileManifest, filePath string, cfg *config.Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	s, err := h.NewStream(ctx, relayID, protocol.DiscoveryProtocolID)
	if err != nil {
		return err
	}
	defer s.Close()

	// Determine advertised address and port
	advertisedAddr := cfg.BindAddress
	advertisedPort := cfg.TransportPort

	if cfg.AdvertisedAddress != "" {
		// Use configured advertised address (e.g., VPS public IP)
		advertisedAddr = cfg.AdvertisedAddress
		log.Printf("[seed] Using configured advertised address: %s", advertisedAddr)
	} else {
		// Fallback to STUN discovery
		publicIP, publicPort, err := getPublicIdentity("stun.l.google.com:19302")
		if err == nil {
			advertisedAddr = publicIP
			// We use the discovered public port for the mapping.
			// Note: This assumes the NAT is port-preserving or we have a mapping alive.
			advertisedPort = publicPort
			log.Printf("[seed] Discovered public identity via STUN: %s:%d", publicIP, publicPort)
		} else {
			log.Printf("[seed] Warning: No advertised address configured and STUN failed, using bind address")
		}
	}

	// Add advertised addresses
	addresses := []string{
		fmt.Sprintf("/ip4/%s/tcp/%d", advertisedAddr, advertisedPort),
		fmt.Sprintf("/ip4/%s/udp/%d/quic-v1", advertisedAddr, advertisedPort),
	}

	log.Printf("[seed] Publishing addresses: %v", addresses)

	payload := protocol.LegacyPublishFilePayload{
		PeerID:     h.ID().String(),
		FileHash:   manifest.FileHash,
		FileName:   filepath.Base(filePath),
		FileSize:   manifest.FileSize,
		ChunkCount: manifest.TotalChunks,
		Addresses:  addresses,
	}

	msg, err := protocol.NewMessage(protocol.LegacyMsgPublishFile, payload)
	if err != nil {
		return err
	}

	data, _ := msg.Encode()
	writer := bufio.NewWriter(s)
	writer.Write(data)
	writer.WriteByte('\n')
	writer.Flush()

	// Wait for Ack
	reader := bufio.NewReader(s)
	ackData, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("failed to read publish ack: %w", err)
	}

	ackMsg, err := protocol.DecodeMessage(ackData)
	if err != nil {
		return fmt.Errorf("failed to decode publish ack: %w", err)
	}

	if ackMsg.Type == protocol.LegacyMsgError {
		var errorPayload protocol.LegacyErrorPayload
		ackMsg.ParsePayload(&errorPayload)
		return fmt.Errorf("relay error: %s", errorPayload.Message)
	}

	log.Printf("[seed] Published successfully to relay (with %d addresses).", len(addresses))
	return nil
}

// getPublicIdentity queries a STUN server to find the external IP and port
func getPublicIdentity(stunServer string) (string, int, error) {
	conn, err := net.Dial("udp", stunServer)
	if err != nil {
		return "", 0, err
	}
	defer conn.Close()

	// STUN Binding Request (20 bytes)
	// [0-1] Type, [2-3] Length, [4-7] Magic Cookie, [8-19] Transaction ID
	message := make([]byte, 20)
	message[0], message[1] = 0x00, 0x01
	message[2], message[3] = 0x00, 0x00
	message[4], message[5], message[6], message[7] = 0x21, 0x12, 0xA4, 0x42
	for i := 8; i < 20; i++ {
		message[i] = byte(i + 42)
	}

	if _, err := conn.Write(message); err != nil {
		return "", 0, err
	}

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp := make([]byte, 1024)
	n, err := conn.Read(resp)
	if err != nil {
		return "", 0, err
	}

	// Parse Attributes (starting at offset 20)
	offset := 20
	for offset < n-4 {
		attrType := uint16(resp[offset])<<8 | uint16(resp[offset+1])
		attrLen := uint16(resp[offset+2])<<8 | uint16(resp[offset+3])
		if attrType == 0x0020 { // XOR-MAPPED-ADDRESS
			port := uint16(resp[offset+6])<<8 | uint16(resp[offset+7])
			port ^= 0x2112
			ip := make([]byte, 4)
			for i := 0; i < 4; i++ {
				ip[i] = resp[offset+8+i] ^ message[4+i]
			}
			return fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], ip[3]), int(port), nil
		}
		offset += 4 + int(attrLen)
	}
	return "", 0, fmt.Errorf("XOR-MAPPED-ADDRESS attribute not found in response")
}

func sendHeartbeat(h host.Host, relayID peer.ID) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s, err := h.NewStream(ctx, relayID, protocol.DiscoveryProtocolID)
	if err != nil {
		return err
	}
	defer s.Close()

	payload := protocol.LegacyHeartbeatPayload{
		PeerID: h.ID().String(),
	}

	msg, err := protocol.NewMessage(protocol.LegacyMsgHeartbeat, payload)
	if err != nil {
		return err
	}

	data, _ := msg.Encode()
	writer := bufio.NewWriter(s)
	writer.Write(data)
	writer.WriteByte('\n')
	writer.Flush()

	return nil
}
