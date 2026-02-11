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
	magnetkLink "magnetk/pkg/magnetk"
	"magnetk/pkg/protocol"
	"magnetk/pkg/transport"
)

func main() {
	relayAddr := flag.String("relay", "", "Relay multiaddr (required)")
	filePath := flag.String("file", "", "File to seed (required)")
	configPath := flag.String("config", "./config/settings.conf", "Path to config file")
	flag.Parse()

	if *relayAddr == "" || *filePath == "" {
		fmt.Println("Usage: seed --relay <relay_multiaddr> --file <file_path>")
		fmt.Println("  --relay  Relay node multiaddr (e.g., /ip4/127.0.0.1/tcp/4001/p2p/<peerID>)")
		fmt.Println("  --file   Path to the file to share")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Printf("[seed] Warning: Could not load config: %v (using defaults)", err)
		cfg = config.DefaultConfig()
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

	// ===== 4. Connect to relay and publish file =====
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

	// Generate magnet link
	ml := &magnetkLink.MagnetLink{
		FileHash:  manifest.FileHash,
		FileName:  filepath.Base(*filePath),
		FileSize:  manifest.FileSize,
		SeedID:    h.ID().String(),
		RelayAddr: *relayAddr,
	}

	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════╗")
	fmt.Println("║  FILE PUBLISHED SUCCESSFULLY!                ║")
	fmt.Println("╚══════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("  Magnetk Link: %s\n\n", ml.String())
	fmt.Println("  Share this link to allow others to download!")
	fmt.Println()

	// ===== 5. Heartbeat loop =====
	go func() {
		ticker := time.NewTicker(time.Duration(cfg.PingIntervalSec) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			// Send heartbeat to relay
			if err := sendHeartbeat(h, relayInfo.ID); err != nil {
				log.Printf("[seed] Heartbeat failed: %v", err)
			}
		}
	}()

	// ===== 6. Wait for shutdown =====
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := h.NewStream(ctx, relayID, protocol.DiscoveryProtocolID)
	if err != nil {
		return err
	}
	defer s.Close()

	payload := protocol.LegacyPublishFilePayload{
		PeerID:     h.ID().String(),
		FileHash:   manifest.FileHash,
		FileName:   filepath.Base(filePath),
		FileSize:   manifest.FileSize,
		ChunkCount: manifest.TotalChunks,
		// Advertise our MsQuic transport address
		Addresses: []string{fmt.Sprintf("/ip4/%s/udp/%d/quic-v1", cfg.EffectiveBindAddress(), cfg.TransportPort)},
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

	log.Printf("[seed] Published successfully to relay.")
	return nil
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
