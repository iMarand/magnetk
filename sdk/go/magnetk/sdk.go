// package magnetk provides the Go SDK for the Magnet P2P file transfer system.
//
// The SDK provides two main types:
//   - Seed: for hosting and sharing files
//   - Client: for discovering and downloading files
//
// Both types use the MsQuic-based transport layer for high-performance
// QUIC connections, with libp2p used for relay discovery.
//
// Quick Start - Hosting a file:
//
//	seed, err := magnetk.NewSeed(magnetk.SeedConfig{
//	    RelayAddr:     "/ip4/1.2.3.4/tcp/4001/p2p/<relay_id>",
//	    TransportPort: 4002,
//	})
//	link, err := seed.PublishFile("./myfile.zip")
//	fmt.Println(link) // Share this magnet link
//	defer seed.Close()
//
// Quick Start - Downloading a file:
//
//	client, err := magnetk.NewClient(magnetk.ClientConfig{})
//	err = client.Download(magnetLink, "./downloads/", func(p magnet.Progress) {
//	    fmt.Printf("%.1f%%\n", p.Percent)
//	})
//	defer client.Close()
package magnetk

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"magnetk/pkg/chunk"
	"magnetk/pkg/config"
	magnetLink "magnetk/pkg/magnetk"
	"magnetk/pkg/protocol"
	"magnetk/pkg/state"
	"magnetk/pkg/transport"
	"magnetk/pkg/types"
)

// ===== Configuration =====

// SeedConfig configures a Seed node.
type SeedConfig struct {
	RelayAddr     string // Relay multiaddr (required)
	BindAddress   string // Local bind address (default: "0.0.0.0")
	TransportPort int    // MsQuic port for serving data (default: 4002)
	ChunkSizeMB   int    // Chunk size in MB (default: 1)
	ConfigPath    string // Path to settings.conf (empty = defaults)
}

// ClientConfig configures a Client.
type ClientConfig struct {
	BindAddress    string // Local bind address (default: "0.0.0.0")
	OutputDir      string // Download output directory (default: ".")
	MaxConnections int    // Max concurrent connections (default: 10)
	ConfigPath     string // Path to settings.conf (empty = defaults)
}

// Progress reports download progress.
type Progress struct {
	FileName    string
	FileSize    int64
	Downloaded  int64
	TotalChunks uint32
	DoneChunks  uint32
	Percent     float64
	SpeedMBps   float64
	ETA         time.Duration
}

// ProgressCallback is called periodically during download.
type ProgressCallback func(Progress)

// ===== Seed =====

// Seed hosts files and serves them to peers via MsQuic.
type Seed struct {
	cfg       *config.Config
	transport *transport.Transport
	listener  *transport.Listener
	chunkMgr  *chunk.Manager
	files     map[string]*seedFile // fileHash -> file info
	mu        sync.RWMutex
	closed    bool
}

type seedFile struct {
	FilePath   string
	Manifest   *chunk.FileManifest
	MagnetLink string
}

// NewSeed creates a new seed node.
func NewSeed(seedCfg SeedConfig) (*Seed, error) {
	// Load config
	cfg := config.DefaultConfig()
	if seedCfg.ConfigPath != "" {
		loaded, err := config.Load(seedCfg.ConfigPath)
		if err == nil {
			cfg = loaded
		}
	}

	// Apply overrides
	if seedCfg.BindAddress != "" {
		cfg.BindAddress = seedCfg.BindAddress
	}
	if seedCfg.TransportPort > 0 {
		cfg.TransportPort = seedCfg.TransportPort
	}
	if seedCfg.ChunkSizeMB > 0 {
		cfg.ChunkSizeMB = seedCfg.ChunkSizeMB
	}
	if seedCfg.RelayAddr != "" {
		cfg.RelayAddress = seedCfg.RelayAddr
	}

	// Create transport
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
		return nil, fmt.Errorf("init transport: %w", err)
	}

	s := &Seed{
		cfg:       cfg,
		transport: quicTransport,
		chunkMgr:  chunk.NewManager(cfg.ChunkSizeBytes()),
		files:     make(map[string]*seedFile),
	}

	// Start listener
	listener, err := quicTransport.Listen(s.handleStream)
	if err != nil {
		quicTransport.Close()
		return nil, fmt.Errorf("start listener: %w", err)
	}
	s.listener = listener

	return s, nil
}

// PublishFile generates a manifest and starts serving a file.
// Returns the magnet link for sharing.
func (s *Seed) PublishFile(filePath string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return "", fmt.Errorf("seed is closed")
	}

	// Generate manifest
	manifest, err := s.chunkMgr.GenerateManifest(filePath)
	if err != nil {
		return "", fmt.Errorf("generate manifest: %w", err)
	}

	// Create magnet link
	ml := &magnetkLink.MagnetLink{
		FileHash:  manifest.FileHash,
		FileName:  filepath.Base(filePath),
		FileSize:  manifest.FileSize,
		RelayAddr: s.cfg.RelayAddress,
	}

	sf := &seedFile{
		FilePath:   filePath,
		Manifest:   manifest,
		MagnetLink: ml.String(),
	}

	s.files[manifest.FileHash] = sf

	log.Printf("[seed-sdk] Published: %s (%d chunks)", filepath.Base(filePath), manifest.TotalChunks)
	return sf.MagnetLink, nil
}

// handleStream processes incoming chunk requests
func (s *Seed) handleStream(stream *transport.Stream, conn *transport.Connection) {
	defer stream.Close()

	reader := protocol.NewFrameReader(stream)
	writer := protocol.NewFrameWriter(stream)

	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			if err == io.EOF {
				return
			}
			return
		}

		switch frame.Type {
		case protocol.MsgManifestReq:
			var req protocol.ManifestRequestPayload
			if err := json.Unmarshal(frame.Payload, &req); err != nil {
				continue
			}

			s.mu.RLock()
			sf, ok := s.files[req.FileHash]
			s.mu.RUnlock()
			if !ok {
				writer.WriteJSON(protocol.MsgError, protocol.ErrorPayload{
					Code: 404, Message: "file not found",
				})
				continue
			}

			resp := protocol.ManifestResponsePayload{
				FileHash:    sf.Manifest.FileHash,
				FileName:    sf.Manifest.FileName,
				FileSize:    sf.Manifest.FileSize,
				ChunkSize:   sf.Manifest.ChunkSize,
				TotalChunks: sf.Manifest.TotalChunks,
				Chunks:      make([]protocol.ChunkMeta, len(sf.Manifest.Chunks)),
			}
			for i, c := range sf.Manifest.Chunks {
				resp.Chunks[i] = protocol.ChunkMeta{
					Index: c.Index, Hash: c.Hash,
					Offset: c.Offset, Size: c.Size,
				}
			}

			writer.WriteJSON(protocol.MsgManifestRes, resp)

		case protocol.MsgChunkReq:
			var req protocol.ChunkRequestPayload
			if err := json.Unmarshal(frame.Payload, &req); err != nil {
				continue
			}

			s.mu.RLock()
			sf, ok := s.files[req.FileHash]
			s.mu.RUnlock()
			if !ok {
				continue
			}

			for _, idx := range req.ChunkIndices {
				data, err := s.chunkMgr.ReadChunk(sf.FilePath, sf.Manifest, idx)
				if err != nil {
					continue
				}
				writer.WriteChunk(idx, data)
			}
		}
	}
}

// Close shuts down the seed.
func (s *Seed) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.listener != nil {
		s.listener.Close()
	}
	if s.transport != nil {
		s.transport.Close()
	}
}

// ===== Client =====

// Client discovers and downloads files from the Magnet network.
type Client struct {
	cfg      *config.Config
	stateMgr *state.Manager
	closed   bool
	mu       sync.Mutex
}

// NewClient creates a new download client.
func NewClient(clientCfg ClientConfig) (*Client, error) {
	cfg := config.DefaultConfig()
	if clientCfg.ConfigPath != "" {
		loaded, err := config.Load(clientCfg.ConfigPath)
		if err == nil {
			cfg = loaded
		}
	}

	if clientCfg.BindAddress != "" {
		cfg.BindAddress = clientCfg.BindAddress
	}
	if clientCfg.MaxConnections > 0 {
		cfg.MaxConnections = clientCfg.MaxConnections
	}

	stateMgr := state.NewManager(cfg.StateDir)

	return &Client{
		cfg:      cfg,
		stateMgr: stateMgr,
	}, nil
}

// Download downloads a file from a magnet link.
func (c *Client) Download(magnetURI string, outputDir string, onProgress ProgressCallback) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("client is closed")
	}
	c.mu.Unlock()

	// Parse magnet link
	ml, err := magnetkLink.Parse(magnetURI)
	if err != nil {
		return fmt.Errorf("parse magnet link: %w", err)
	}

	outFile := filepath.Join(outputDir, ml.FileName)

	// Check for resume state
	existingState, _ := c.stateMgr.FindByFileHash(ml.FileHash)
	completedMap := make(map[uint32]bool)
	if existingState != nil {
		for _, idx := range existingState.CompletedChunks {
			completedMap[idx] = true
		}
	}

	// Create transport
	transportCfg := transport.Config{
		BindAddress:       c.cfg.EffectiveBindAddress(),
		Port:              0, // Any port
		NoCertValidation:  c.cfg.NoCertValidation,
		ALPN:              "magnetk",
		MaxConnections:    uint32(c.cfg.MaxConnections),
		IdleTimeoutMs:     60000,
		SendBufferSize:    c.cfg.SendBufferSizeBytes(),
		RecvBufferSize:    c.cfg.RecvBufferSizeBytes(),
		MaxStreamsPerConn: uint16(c.cfg.MaxStreamsPerConnection),
		MaxThroughput:     true,
	}

	quicTransport, err := transport.NewTransport(transportCfg)
	if err != nil {
		return fmt.Errorf("init transport: %w", err)
	}
	defer quicTransport.Close()

	// TODO: Connect to relay to discover seeds, then connect to seed
	// For now, connect directly if we have seed address
	if ml.SeedID == "" {
		return fmt.Errorf("no seed address available (relay lookup not yet implemented)")
	}

	// Connect to seed
	host := "127.0.0.1" // placeholder
	port := uint16(4002)
	conn, err := quicTransport.Connect(host, port)
	if err != nil {
		return fmt.Errorf("connect to seed: %w", err)
	}
	defer conn.Close()

	stream, err := conn.OpenStream()
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()

	reader := protocol.NewFrameReader(stream)
	writer := protocol.NewFrameWriter(stream)

	// Request manifest
	if err := writer.WriteJSON(protocol.MsgManifestReq, protocol.ManifestRequestPayload{
		FileHash: ml.FileHash,
	}); err != nil {
		return fmt.Errorf("request manifest: %w", err)
	}

	frame, err := reader.ReadFrame()
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	var manifest protocol.ManifestResponsePayload
	if err := json.Unmarshal(frame.Payload, &manifest); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}

	// Determine needed chunks
	var needed []uint32
	for i := uint32(0); i < manifest.TotalChunks; i++ {
		if !completedMap[i] {
			needed = append(needed, i)
		}
	}

	// Open output file
	file, err := os.OpenFile(outFile, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	defer file.Close()

	if manifest.FileSize > 0 {
		file.Truncate(manifest.FileSize)
	}

	// Track state
	ts := &types.TransferState{
		TransferID:      fmt.Sprintf("sdk-%s-%d", ml.FileHash[:8], time.Now().Unix()),
		FileHash:        ml.FileHash,
		FileName:        manifest.FileName,
		FileSize:        manifest.FileSize,
		TotalChunks:     manifest.TotalChunks,
		CompletedChunks: make([]uint32, 0),
		OutputPath:      outFile,
	}

	// Download chunks
	startTime := time.Now()
	downloaded := 0
	batchSize := c.cfg.MaxParallelChunks

	for i := 0; i < len(needed); i += batchSize {
		end := i + batchSize
		if end > len(needed) {
			end = len(needed)
		}
		batch := needed[i:end]

		writer.WriteJSON(protocol.MsgChunkReq, protocol.ChunkRequestPayload{
			FileHash: ml.FileHash, ChunkIndices: batch,
		})

		for range batch {
			frame, err := reader.ReadFrame()
			if err != nil {
				return fmt.Errorf("read chunk: %w", err)
			}
			if frame.Type != protocol.MsgChunkRes {
				continue
			}

			idx, data, err := protocol.ParseChunkPayload(frame.Payload)
			if err != nil {
				continue
			}

			offset := int64(idx) * manifest.ChunkSize
			file.WriteAt(data, offset)
			downloaded++
			ts.CompletedChunks = append(ts.CompletedChunks, idx)

			if onProgress != nil {
				elapsed := time.Since(startTime).Seconds()
				speed := float64(downloaded) * float64(manifest.ChunkSize) / elapsed / 1024 / 1024
				remaining := float64(len(needed)-downloaded) * float64(manifest.ChunkSize) / (speed * 1024 * 1024)

				onProgress(Progress{
					FileName:    manifest.FileName,
					FileSize:    manifest.FileSize,
					Downloaded:  int64(downloaded) * manifest.ChunkSize,
					TotalChunks: manifest.TotalChunks,
					DoneChunks:  uint32(downloaded + len(completedMap)),
					Percent:     float64(downloaded+len(completedMap)) / float64(manifest.TotalChunks) * 100,
					SpeedMBps:   speed,
					ETA:         time.Duration(remaining) * time.Second,
				})
			}

			if downloaded%10 == 0 {
				c.stateMgr.Save(ts)
			}
		}
	}

	c.stateMgr.Save(ts)

	// Verify
	file.Close()
	valid, err := chunk.VerifyFile(outFile, ml.FileHash)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if !valid {
		return fmt.Errorf("file hash mismatch")
	}

	c.stateMgr.Delete(ts.TransferID)
	return nil
}

// Close shuts down the client.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

// ParseMagnetLink parses a magnet URI string.
func ParseMagnetLink(uri string) (*magnetkLink.MagnetLink, error) {
	return magnetkLink.Parse(uri)
}
