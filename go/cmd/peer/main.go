// Peer client for the Magnet P2P network.
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
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"

	"github.com/iMarand/magnetk/pkg/chunk"
	"github.com/iMarand/magnetk/pkg/config"
	magnetLink "github.com/iMarand/magnetk/pkg/transport"
	"github.com/iMarand/magnetk/pkg/protocol"
	"github.com/iMarand/magnetk/pkg/state"
	"github.com/iMarand/magnetk/pkg/transport"
	"github.com/iMarand/magnetk/pkg/types"
)

func main() {
	magnetURI := flag.String("magnet", "", "Magnet link to download")
	relayAddr := flag.String("relay", "", "Relay multiaddr (if not in magnet link)")
	outputPath := flag.String("output", "", "Output file path (default: current directory)")
	configPath := flag.String("config", "./config/settings.conf", "Path to config file")
	flag.Parse()

	if *magnetURI == "" {
		fmt.Println("Usage: peer --magnet <magnet_link> [--output <path>] [--relay <addr>]")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  --magnet   Magnet link (required)")
		fmt.Println("  --output   Output file path (default: filename from magnet)")
		fmt.Println("  --relay    Override relay address")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Printf("[peer] Warning: Could not load config: %v (using defaults)", err)
		cfg = config.DefaultConfig()
	}

	fmt.Println("╔══════════════════════════════════════════════╗")
	fmt.Println("║         MAGNET P2P PEER CLIENT               ║")
	fmt.Println("║         Hybrid MsQuic + libp2p              ║")
	fmt.Println("╚══════════════════════════════════════════════╝")
	fmt.Println()

	// ===== 1. Parse magnet link =====
	ml, err := magnetLink.Parse(*magnetURI)
	if err != nil {
		log.Fatalf("[peer] Invalid magnet link: %v", err)
	}

	log.Printf("[peer] File: %s", ml.FileName)
	log.Printf("[peer] Hash: %s", ml.FileHash)
	if ml.FileSize > 0 {
		log.Printf("[peer] Size: %d bytes", ml.FileSize)
	}

	// Determine relay address
	relay := ml.RelayAddr
	if *relayAddr != "" {
		relay = *relayAddr
	}
	if relay == "" {
		log.Fatal("[peer] No relay address available. Provide via --relay or in magnet link.")
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

	// ===== 3. Start libp2p for relay discovery =====
	listenAddr := fmt.Sprintf("/ip4/%s/tcp/0", cfg.EffectiveBindAddress())
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Security(noise.ID, noise.New),
	)
	if err != nil {
		log.Fatalf("[peer] Failed to create libp2p host: %v", err)
	}
	defer h.Close()

	log.Printf("[peer] Peer ID: %s", h.ID().String())

	// ===== 4. Connect to relay =====
	relayMA, err := multiaddr.NewMultiaddr(relay)
	if err != nil {
		log.Fatalf("[peer] Invalid relay address: %v", err)
	}

	relayInfo, err := peer.AddrInfoFromP2pAddr(relayMA)
	if err != nil {
		log.Fatalf("[peer] Failed to parse relay address: %v", err)
	}

	if err := h.Connect(context.Background(), *relayInfo); err != nil {
		log.Fatalf("[peer] Failed to connect to relay: %v", err)
	}
	log.Println("[peer] Connected to relay")

	// ===== 5. Lookup seeds =====
	seeds, err := lookupSeeds(h, relayInfo.ID, ml.FileHash)
	if err != nil || len(seeds) == 0 {
		log.Fatalf("[peer] No seeds found for file %s", ml.FileHash[:12])
	}
	log.Printf("[peer] Found %d seed(s)", len(seeds))

	// ===== 6. Start download via MsQuic =====
	// Set up download with cancel support
	doneCh := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		defer close(doneCh)
		err := downloadFile(seeds[0], ml, outFile, cfg, stateMgr, existingState)
		if err != nil {
			log.Printf("[peer] Download failed: %v", err)
			return
		}
	}()

	select {
	case <-doneCh:
		// Download completed
	case <-sigCh:
		log.Println("[peer] Download interrupted. State saved for resume.")
	}
}

// downloadFile downloads a file from a seed via MsQuic
func downloadFile(
	seed protocol.SeedInfo,
	ml *magnetLink.MagnetLink,
	outFile string,
	cfg *config.Config,
	stateMgr *state.Manager,
	existingState *types.TransferState,
) error {
	log.Printf("[peer] Connecting to seed %s via MsQuic at %s",
		seed.PeerID[:12], seed.TransportAddr)

	// Create MsQuic transport (client mode)
	transportCfg := transport.Config{
		BindAddress:       cfg.EffectiveBindAddress(),
		Port:              0, // Any available port for client
		NoCertValidation:  cfg.NoCertValidation,
		ALPN:              "magnet",
		MaxConnections:    10,
		IdleTimeoutMs:     60000,
		SendBufferSize:    cfg.SendBufferSizeBytes(),
		RecvBufferSize:    cfg.RecvBufferSizeBytes(),
		MaxStreamsPerConn: uint16(cfg.MaxStreamsPerConnection),
		MaxThroughput:     true,
	}

	quicTransport, err := transport.NewTransport(transportCfg)
	if err != nil {
		return fmt.Errorf("init transport: %w", err)
	}
	defer quicTransport.Close()

	// Connect to seed
	// Parse host:port from transport addr
	host, port := parseHostPort(seed.TransportAddr)
	conn, err := quicTransport.Connect(host, port)
	if err != nil {
		return fmt.Errorf("connect to seed: %w", err)
	}
	defer conn.Close()

	// Open stream
	stream, err := conn.OpenStream()
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()

	reader := protocol.NewFrameReader(stream)
	writer := protocol.NewFrameWriter(stream)

	// ===== Request manifest =====
	log.Println("[peer] Requesting manifest...")
	err = writer.WriteJSON(protocol.MsgManifestReq, protocol.ManifestRequestPayload{
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
			frame, err := reader.ReadFrame()
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
			transferState.CompletedChunks = append(transferState.CompletedChunks, chunkIdx)

			// Progress
			pct := float64(downloaded+len(completedMap)) / float64(manifest.TotalChunks) * 100
			elapsed := time.Since(startTime).Seconds()
			speed := float64(downloaded) * float64(manifest.ChunkSize) / elapsed / 1024 / 1024

			fmt.Printf("\r[peer] Progress: %.1f%% (%d/%d chunks) | %.2f MB/s",
				pct, downloaded+len(completedMap), manifest.TotalChunks, speed)

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

func lookupSeeds(h interface{}, relayID peer.ID, fileHash string) ([]protocol.SeedInfo, error) {
	// TODO: Implement discovery via libp2p stream to relay
	log.Printf("[peer] Looking up seeds for %s...", fileHash[:12])
	return nil, fmt.Errorf("no seeds found")
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
