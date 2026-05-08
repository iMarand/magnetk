// Package magnetk provides the Go SDK for Magnetk P2P file transfer.
//
// The SDK exposes the same network paths as the CLI:
//   - direct MsQuic with TCP fallback
//   - relay discovery through a public relay
//   - libp2p circuit relay fallback
//   - libp2p QUIC/DCUtR hole punching when both networks allow it
package magnetk

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	libp2pProto "github.com/libp2p/go-libp2p/core/protocol"
	circuitv2client "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"

	"magnetk/pkg/chunk"
	"magnetk/pkg/config"
	magnetkLink "magnetk/pkg/magnetk"
	magnetkNAT "magnetk/pkg/nat"
	magnetkNet "magnetk/pkg/network"
	"magnetk/pkg/protocol"
	"magnetk/pkg/state"
	"magnetk/pkg/transport"
	"magnetk/pkg/types"
)

// SeedConfig configures a Seed node.
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

// ClientConfig configures a Client.
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

// ProgressCallback is called during download, including while a large chunk is
// still being read over a relayed stream.
type ProgressCallback func(Progress)

// Seed hosts files and serves them to peers.
type Seed struct {
	cfg         *config.Config
	transport   *transport.Transport
	listener    *transport.Listener
	tcpListener net.Listener
	host        host.Host
	relayID     peer.ID
	relayAddr   string
	natMappings []*magnetkNAT.Mapping
	chunkMgr    *chunk.Manager
	files       map[string]*seedFile
	stopCh      chan struct{}
	closeOnce   sync.Once
	mu          sync.RWMutex
	closed      bool
}

type seedFile struct {
	FilePath   string
	Manifest   *chunk.FileManifest
	MagnetLink string
}

// NewSeed creates a new seed node and starts all configured listeners.
func NewSeed(seedCfg SeedConfig) (*Seed, error) {
	cfg, err := loadSeedConfig(seedCfg)
	if err != nil {
		return nil, err
	}

	relayAddr, err := normalizeRelayAddr(cfg.RelayAddress)
	if err != nil {
		return nil, fmt.Errorf("resolve relay address: %w", err)
	}
	cfg.RelayAddress = relayAddr

	if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
		cert, key, err := config.EnsureDevCertificates(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("ensure dev certificates: %w", err)
		}
		cfg.TLSCertFile = cert
		cfg.TLSKeyFile = key
	}

	p2pHost, err := createLibp2pHost(cfg)
	if err != nil {
		return nil, fmt.Errorf("create libp2p host: %w", err)
	}

	quicTransport, err := newDataTransport(cfg, cfg.TransportPort)
	if err != nil {
		p2pHost.Close()
		return nil, fmt.Errorf("init transport: %w", err)
	}

	s := &Seed{
		cfg:       cfg,
		transport: quicTransport,
		host:      p2pHost,
		relayAddr: relayAddr,
		chunkMgr:  chunk.NewManager(cfg.ChunkSizeBytes()),
		files:     make(map[string]*seedFile),
		stopCh:    make(chan struct{}),
	}

	listener, err := quicTransport.Listen(s.handleMsQuicStream)
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("start MsQuic listener: %w", err)
	}
	s.listener = listener

	tcpListener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", cfg.EffectiveBindAddress(), cfg.TransportPort))
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("start TCP fallback listener: %w", err)
	}
	s.tcpListener = tcpListener
	go s.acceptTCP()

	if cfg.HolePunch {
		s.natMappings = magnetkNAT.Open(cfg.TransportPort, "magnetk-sdk-seed")
	}

	s.host.SetStreamHandler(libp2pProto.ID(protocol.MagnetProtocolID), s.handleLibp2pStream)

	if relayAddr != "" {
		if err := s.connectRelay(relayAddr); err != nil {
			s.Close()
			return nil, err
		}
		go s.heartbeatLoop()
	}

	log.Printf("[magnetk-sdk] Seed peer ID: %s", s.host.ID())
	return s, nil
}

// PublishFile generates a manifest, registers the file with the relay, and
// returns a shareable magnetk link.
func (s *Seed) PublishFile(filePath string) (string, error) {
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return "", fmt.Errorf("seed is closed")
	}

	manifest, err := s.chunkMgr.GenerateManifest(filePath)
	if err != nil {
		return "", fmt.Errorf("generate manifest: %w", err)
	}

	ml := &magnetkLink.MagnetLink{
		FileHash:   manifest.FileHash,
		FileName:   filepath.Base(filePath),
		FileSize:   manifest.FileSize,
		SeedID:     s.host.ID().String(),
		RelayAddr:  s.relayAddr,
		DHTEnabled: s.cfg.DHTEnabled,
	}

	sf := &seedFile{
		FilePath:   filePath,
		Manifest:   manifest,
		MagnetLink: ml.String(),
	}

	s.mu.Lock()
	s.files[manifest.FileHash] = sf
	s.mu.Unlock()

	if s.relayAddr != "" {
		if err := s.publishToRelay(manifest, filePath); err != nil {
			return "", fmt.Errorf("publish to relay: %w", err)
		}
	}

	log.Printf("[magnetk-sdk] Published %s (%d chunks)", filepath.Base(filePath), manifest.TotalChunks)
	return sf.MagnetLink, nil
}

// Close shuts down the seed and removes NAT mappings.
func (s *Seed) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()

		close(s.stopCh)
		if s.tcpListener != nil {
			s.tcpListener.Close()
		}
		if s.listener != nil {
			s.listener.Close()
		}
		if s.transport != nil {
			s.transport.Close()
		}
		if s.host != nil {
			s.host.Close()
		}
		for _, m := range s.natMappings {
			m.Close()
		}
	})
}

func (s *Seed) connectRelay(relayAddr string) error {
	relayMA, err := multiaddr.NewMultiaddr(relayAddr)
	if err != nil {
		return fmt.Errorf("invalid relay multiaddr: %w", err)
	}
	relayInfo, err := peer.AddrInfoFromP2pAddr(relayMA)
	if err != nil {
		return fmt.Errorf("parse relay peer info: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.host.Connect(ctx, *relayInfo); err != nil {
		return fmt.Errorf("connect to relay: %w", err)
	}

	reserveCtx, reserveCancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, err = circuitv2client.Reserve(reserveCtx, s.host, *relayInfo)
	reserveCancel()
	if err != nil {
		log.Printf("[magnetk-sdk] Warning: circuit-relay reservation failed: %v", err)
	}

	s.relayID = relayInfo.ID
	return nil
}

func (s *Seed) publishToRelay(manifest *chunk.FileManifest, filePath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	stream, err := s.host.NewStream(ctx, s.relayID, libp2pProto.ID(protocol.DiscoveryProtocolID))
	if err != nil {
		return err
	}
	defer stream.Close()

	addresses := buildSeedAddresses(s.cfg, s.natMappings)
	payload := protocol.LegacyPublishFilePayload{
		PeerID:     s.host.ID().String(),
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
	data, err := msg.Encode()
	if err != nil {
		return err
	}

	writer := bufio.NewWriter(stream)
	if _, err := writer.Write(data); err != nil {
		return err
	}
	if err := writer.WriteByte('\n'); err != nil {
		return err
	}
	if err := writer.Flush(); err != nil {
		return err
	}

	ackData, err := bufio.NewReader(stream).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read publish ack: %w", err)
	}
	ackMsg, err := protocol.DecodeMessage(ackData)
	if err != nil {
		return fmt.Errorf("decode publish ack: %w", err)
	}
	if ackMsg.Type == protocol.LegacyMsgError {
		var ep protocol.LegacyErrorPayload
		_ = ackMsg.ParsePayload(&ep)
		return fmt.Errorf("relay error: %s", ep.Message)
	}
	return nil
}

func (s *Seed) heartbeatLoop() {
	interval := time.Duration(s.cfg.PingIntervalSec) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := sendHeartbeat(s.host, s.relayID); err != nil {
				log.Printf("[magnetk-sdk] Heartbeat failed: %v", err)
			}
		case <-s.stopCh:
			return
		}
	}
}

func (s *Seed) acceptTCP() {
	for {
		conn, err := s.tcpListener.Accept()
		if err != nil {
			select {
			case <-s.stopCh:
				return
			default:
				log.Printf("[magnetk-sdk] TCP accept error: %v", err)
				continue
			}
		}
		go s.handleTCPConn(conn)
	}
}

func (s *Seed) handleMsQuicStream(stream *transport.Stream, _ *transport.Connection) {
	defer stream.Close()
	s.serveChunkRequest(protocol.NewFrameReader(stream), protocol.NewFrameWriter(stream), "MsQuic")
}

func (s *Seed) handleTCPConn(conn net.Conn) {
	defer conn.Close()
	s.serveChunkRequest(protocol.NewFrameReader(conn), protocol.NewFrameWriter(conn), "TCP")
}

func (s *Seed) handleLibp2pStream(stream network.Stream) {
	defer stream.Close()
	s.serveChunkRequest(protocol.NewFrameReader(stream), protocol.NewFrameWriter(stream), "libp2p-relay")
}

func (s *Seed) serveChunkRequest(reader *protocol.FrameReader, writer *protocol.FrameWriter, transportType string) {
	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			if err != io.EOF {
				log.Printf("[magnetk-sdk] Error reading frame (%s): %v", transportType, err)
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
				_ = writer.WriteJSON(protocol.MsgError, protocol.ErrorPayload{Code: 404, Message: "file not found"})
				continue
			}

			resp := manifestResponse(sf.Manifest)
			if err := writer.WriteJSON(protocol.MsgManifestRes, resp); err != nil {
				log.Printf("[magnetk-sdk] Error sending manifest (%s): %v", transportType, err)
				return
			}

		case protocol.MsgChunkReq:
			var req protocol.ChunkRequestPayload
			if err := json.Unmarshal(frame.Payload, &req); err != nil {
				continue
			}

			s.mu.RLock()
			sf, ok := s.files[req.FileHash]
			s.mu.RUnlock()
			if !ok {
				_ = writer.WriteJSON(protocol.MsgError, protocol.ErrorPayload{Code: 404, Message: "file not found"})
				continue
			}

			for _, idx := range req.ChunkIndices {
				data, err := s.chunkMgr.ReadChunk(sf.FilePath, idx)
				if err != nil {
					_ = writer.WriteJSON(protocol.MsgError, protocol.ErrorPayload{Code: 500, Message: err.Error()})
					continue
				}
				if err := writer.WriteChunk(idx, data.Data); err != nil {
					log.Printf("[magnetk-sdk] Error sending chunk %d (%s): %v", idx, transportType, err)
					return
				}
			}
		}
	}
}

// Client discovers and downloads files from the Magnetk network.
type Client struct {
	cfg      *config.Config
	stateMgr *state.Manager
	closed   bool
	mu       sync.Mutex
}

// NewClient creates a new download client.
func NewClient(clientCfg ClientConfig) (*Client, error) {
	cfg, err := loadClientConfig(clientCfg)
	if err != nil {
		return nil, err
	}

	stateMgr, err := state.NewManager(cfg.StateDir, cfg.StateExpiryDays)
	if err != nil {
		return nil, fmt.Errorf("init state manager: %w", err)
	}

	return &Client{cfg: cfg, stateMgr: stateMgr}, nil
}

// Download downloads a file from a magnet link. outputDir may be a directory
// or a full output file path.
func (c *Client) Download(magnetURI string, outputDir string, onProgress ProgressCallback) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("client is closed")
	}
	c.mu.Unlock()

	ml, err := magnetkLink.Parse(magnetURI)
	if err != nil {
		return fmt.Errorf("parse magnet link: %w", err)
	}

	outFile, err := resolveOutputPath(outputDir, ml.FileName)
	if err != nil {
		return err
	}

	existingState, _ := c.stateMgr.FindByFileHash(ml.FileHash)

	p2pHost, err := createLibp2pHost(c.cfg)
	if err != nil {
		return fmt.Errorf("create libp2p host: %w", err)
	}
	defer p2pHost.Close()

	var seeds []protocol.SeedInfo
	if ml.RelayAddr != "" {
		seeds, err = lookupSeedsViaRelay(ml.RelayAddr, ml.FileHash)
		if err != nil {
			log.Printf("[magnetk-sdk] Relay discovery failed: %v", err)
		}
	}
	if len(seeds) == 0 {
		return fmt.Errorf("no seeds found")
	}

	routable := filterRoutableSeeds(seeds)
	var lastErr error
	for _, seed := range routable {
		if err := c.downloadDirect(seed, ml, outFile, existingState, onProgress); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	for _, seed := range seeds {
		if err := c.downloadViaLibp2p(p2pHost, seed, ml.RelayAddr, ml, outFile, existingState, onProgress); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	if lastErr != nil {
		return fmt.Errorf("all discovery and connection methods exhausted: %w", lastErr)
	}
	return fmt.Errorf("all discovery and connection methods exhausted")
}

// Close shuts down the client.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

func (c *Client) downloadDirect(seed protocol.SeedInfo, ml *magnetkLink.MagnetLink, outFile string, existingState *types.TransferState, onProgress ProgressCallback) error {
	hostAddr, port := parseHostPort(seed.TransportAddr)
	if !isRoutableAddr(hostAddr) {
		return fmt.Errorf("seed address is not routable: %s", seed.TransportAddr)
	}

	mode := strings.ToLower(c.cfg.TransportMode)
	if mode == "" {
		mode = "auto"
	}

	var reader *protocol.FrameReader
	var writer *protocol.FrameWriter
	var cleanup func()
	msquicOK := false

	if mode != "tcp" {
		quicTransport, err := newDataTransport(c.cfg, 0)
		if err == nil {
			conn, err := quicTransport.Connect(hostAddr, port)
			if err == nil {
				stream, err := conn.OpenStream()
				if err == nil {
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
	}

	if !msquicOK && mode != "quic" {
		tcpConn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", hostAddr, port), 10*time.Second)
		if err != nil {
			return fmt.Errorf("connect to seed (TCP fallback): %w", err)
		}
		reader = protocol.NewFrameReader(tcpConn)
		writer = protocol.NewFrameWriter(tcpConn)
		cleanup = func() { tcpConn.Close() }
	}

	if cleanup == nil {
		return fmt.Errorf("direct connection failed")
	}
	defer cleanup()

	return c.runChunkProtocol(reader, writer, ml, outFile, existingState, onProgress)
}

func (c *Client) downloadViaLibp2p(h host.Host, seed protocol.SeedInfo, relayAddr string, ml *magnetkLink.MagnetLink, outFile string, existingState *types.TransferState, onProgress ProgressCallback) error {
	if relayAddr == "" {
		return fmt.Errorf("no relay address for libp2p tunnel")
	}

	relayMA, err := multiaddr.NewMultiaddr(relayAddr)
	if err != nil {
		return fmt.Errorf("invalid relay multiaddr: %w", err)
	}
	relayInfo, err := peer.AddrInfoFromP2pAddr(relayMA)
	if err != nil {
		return fmt.Errorf("parse relay peer info: %w", err)
	}

	connectCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := h.Connect(connectCtx, *relayInfo); err != nil {
		cancel()
		return fmt.Errorf("connect to relay: %w", err)
	}
	cancel()

	seedID, err := peer.Decode(seed.PeerID)
	if err != nil {
		return fmt.Errorf("decode seed peer id: %w", err)
	}

	circuitAddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("%s/p2p-circuit", relayAddr))
	if err != nil {
		return fmt.Errorf("build circuit multiaddr: %w", err)
	}
	h.Peerstore().AddAddr(seedID, circuitAddr, peerstore.TempAddrTTL)

	streamCtx, streamCancel := context.WithTimeout(context.Background(), 30*time.Second)
	streamCtx = network.WithAllowLimitedConn(streamCtx, "magnetk")
	stream, err := h.NewStream(streamCtx, seedID, libp2pProto.ID(protocol.MagnetProtocolID))
	streamCancel()
	if err != nil {
		return fmt.Errorf("open libp2p stream to seed: %w", err)
	}

	if stream.Conn().Stat().Limited {
		if directStream, ok := tryHolePunchedLibp2pStream(h, seedID); ok {
			stream.Close()
			stream = directStream
		} else if strings.ToLower(c.cfg.RelayAssisted) == "false" {
			stream.Close()
			return fmt.Errorf("direct libp2p hole punch unavailable and relay-assisted=false")
		}
	}
	defer stream.Close()

	return c.runChunkProtocol(protocol.NewFrameReader(stream), protocol.NewFrameWriter(stream), ml, outFile, existingState, onProgress)
}

func (c *Client) runChunkProtocol(reader *protocol.FrameReader, writer *protocol.FrameWriter, ml *magnetkLink.MagnetLink, outFile string, existingState *types.TransferState, onProgress ProgressCallback) error {
	if err := writer.WriteJSON(protocol.MsgManifestReq, protocol.ManifestRequestPayload{FileHash: ml.FileHash}); err != nil {
		return fmt.Errorf("send manifest request: %w", err)
	}

	frame, err := reader.ReadFrame()
	if err != nil {
		return fmt.Errorf("read manifest response: %w", err)
	}
	if frame.Type != protocol.MsgManifestRes {
		return fmt.Errorf("unexpected manifest response type: 0x%02x", frame.Type)
	}

	var manifest protocol.ManifestResponsePayload
	if err := json.Unmarshal(frame.Payload, &manifest); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}

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

	var needed []uint32
	for i := uint32(0); i < manifest.TotalChunks; i++ {
		if !completedMap[i] {
			needed = append(needed, i)
		}
	}
	if len(needed) == 0 {
		reportProgress(onProgress, manifest, manifest.FileSize, manifest.TotalChunks, time.Now(), completedBytes)
		return nil
	}

	file, err := os.OpenFile(outFile, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	if manifest.FileSize > 0 {
		_ = file.Truncate(manifest.FileSize)
	}
	defer file.Close()

	ts := &types.TransferState{
		TransferID:      fmt.Sprintf("sdk-%s-%d", ml.FileHash[:8], time.Now().Unix()),
		FileHash:        ml.FileHash,
		FileName:        manifest.FileName,
		FileSize:        manifest.FileSize,
		TotalChunks:     manifest.TotalChunks,
		CompletedChunks: make([]uint32, 0),
		OutputPath:      outFile,
	}
	if existingState != nil {
		ts = existingState
		ts.OutputPath = outFile
	}

	batchSize := c.cfg.MaxParallelChunks
	if batchSize <= 0 {
		batchSize = 1
	}
	start := time.Now()
	downloadedChunks := 0
	downloadedBytes := completedBytes

	for i := 0; i < len(needed); i += batchSize {
		end := i + batchSize
		if end > len(needed) {
			end = len(needed)
		}
		batch := needed[i:end]

		if err := writer.WriteJSON(protocol.MsgChunkReq, protocol.ChunkRequestPayload{FileHash: ml.FileHash, ChunkIndices: batch}); err != nil {
			return fmt.Errorf("send chunk request: %w", err)
		}

		for range batch {
			var partial uint32
			frame, err := reader.ReadFrameWithProgress(func(read, total uint32) {
				if total < 64*1024 || read == partial {
					return
				}
				partial = read
				reportProgress(onProgress, manifest, downloadedBytes+int64(read), uint32(downloadedChunks+len(completedMap)), start, completedBytes)
			})
			if err != nil {
				return fmt.Errorf("read chunk: %w", err)
			}
			if frame.Type == protocol.MsgError {
				var ep protocol.ErrorPayload
				_ = json.Unmarshal(frame.Payload, &ep)
				return fmt.Errorf("server error: %s", ep.Message)
			}
			if frame.Type != protocol.MsgChunkRes {
				return fmt.Errorf("unexpected chunk response type: 0x%02x", frame.Type)
			}

			idx, data, err := protocol.ParseChunkPayload(frame.Payload)
			if err != nil {
				return fmt.Errorf("parse chunk: %w", err)
			}
			if _, err := file.WriteAt(data, int64(idx)*manifest.ChunkSize); err != nil {
				return fmt.Errorf("write chunk %d: %w", idx, err)
			}

			downloadedChunks++
			downloadedBytes += int64(len(data))
			ts.CompletedChunks = append(ts.CompletedChunks, idx)
			reportProgress(onProgress, manifest, downloadedBytes, uint32(downloadedChunks+len(completedMap)), start, completedBytes)

			if downloadedChunks%10 == 0 {
				_ = c.stateMgr.Save(ts)
			}
		}
	}

	_ = c.stateMgr.Save(ts)
	if err := file.Close(); err != nil {
		return err
	}

	valid, err := chunk.VerifyFile(outFile, ml.FileHash)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if !valid {
		return fmt.Errorf("file hash mismatch")
	}
	_ = c.stateMgr.Delete(ts.TransferID)
	return nil
}

// ParseMagnetLink parses a magnet URI string.
func ParseMagnetLink(uri string) (*magnetkLink.MagnetLink, error) {
	return magnetkLink.Parse(uri)
}

func loadSeedConfig(seedCfg SeedConfig) (*config.Config, error) {
	cfg := config.DefaultConfig()
	if seedCfg.ConfigPath != "" {
		loaded, err := config.Load(seedCfg.ConfigPath)
		if err != nil {
			return nil, err
		}
		cfg = loaded
	}
	if seedCfg.BindAddress != "" {
		cfg.BindAddress = seedCfg.BindAddress
	}
	if seedCfg.AdvertisedAddress != "" {
		cfg.AdvertisedAddress = seedCfg.AdvertisedAddress
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
	if seedCfg.RelayAssisted != "" {
		cfg.RelayAssisted = strings.ToLower(seedCfg.RelayAssisted)
	}
	if seedCfg.TransportMode != "" {
		cfg.TransportMode = strings.ToLower(seedCfg.TransportMode)
	}
	if len(seedCfg.RelayAddresses) > 0 {
		cfg.RelayAddresses = seedCfg.RelayAddresses
	}
	cfg.DHTEnabled = seedCfg.DHTEnabled
	if len(seedCfg.DHTBootstrapPeers) > 0 {
		cfg.DHTBootstrapPeers = seedCfg.DHTBootstrapPeers
	}
	if seedCfg.HolePunch {
		cfg.HolePunch = true
	}
	return cfg, nil
}

func loadClientConfig(clientCfg ClientConfig) (*config.Config, error) {
	cfg := config.DefaultConfig()
	if clientCfg.ConfigPath != "" {
		loaded, err := config.Load(clientCfg.ConfigPath)
		if err != nil {
			return nil, err
		}
		cfg = loaded
	}
	if clientCfg.BindAddress != "" {
		cfg.BindAddress = clientCfg.BindAddress
	}
	if clientCfg.MaxConnections > 0 {
		cfg.MaxConnections = clientCfg.MaxConnections
	}
	if clientCfg.RelayAssisted != "" {
		cfg.RelayAssisted = strings.ToLower(clientCfg.RelayAssisted)
	}
	if clientCfg.TransportMode != "" {
		cfg.TransportMode = strings.ToLower(clientCfg.TransportMode)
	}
	if len(clientCfg.RelayAddresses) > 0 {
		cfg.RelayAddresses = clientCfg.RelayAddresses
	}
	cfg.DHTEnabled = clientCfg.DHTEnabled
	if len(clientCfg.DHTBootstrapPeers) > 0 {
		cfg.DHTBootstrapPeers = clientCfg.DHTBootstrapPeers
	}
	if clientCfg.HolePunch {
		cfg.HolePunch = true
	}
	return cfg, nil
}

func createLibp2pHost(cfg *config.Config) (host.Host, error) {
	listenAddrs := []string{
		fmt.Sprintf("/ip4/%s/tcp/0", cfg.EffectiveBindAddress()),
		fmt.Sprintf("/ip4/%s/udp/0/quic-v1", cfg.EffectiveBindAddress()),
	}
	opts := []libp2p.Option{
		libp2p.ListenAddrStrings(listenAddrs...),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Security(noise.ID, noise.New),
	}
	if cfg.HolePunch {
		opts = append(opts, libp2p.NATPortMap(), libp2p.EnableHolePunching())
	}
	return libp2p.New(opts...)
}

func newDataTransport(cfg *config.Config, port int) (*transport.Transport, error) {
	return transport.NewTransport(transport.Config{
		BindAddress:       cfg.EffectiveBindAddress(),
		Port:              uint16(port),
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
	})
}

func manifestResponse(manifest *chunk.FileManifest) protocol.ManifestResponsePayload {
	resp := protocol.ManifestResponsePayload{
		FileHash:    manifest.FileHash,
		FileName:    manifest.FileName,
		FileSize:    manifest.FileSize,
		ChunkSize:   int64(manifest.ChunkSize),
		TotalChunks: manifest.TotalChunks,
		Chunks:      make([]protocol.ChunkMeta, len(manifest.Chunks)),
	}
	for i, c := range manifest.Chunks {
		resp.Chunks[i] = protocol.ChunkMeta{Index: c.Index, Hash: c.Hash, Offset: c.Offset, Size: int64(c.Size)}
	}
	return resp
}

func normalizeRelayAddr(relay string) (string, error) {
	relay = strings.TrimSpace(relay)
	if relay == "" || relay == "0.0.0.0:4001" {
		return "", nil
	}
	if strings.HasPrefix(relay, "/") {
		return relay, nil
	}

	hostPart := relay
	port := 4003
	if h, p, err := net.SplitHostPort(relay); err == nil {
		hostPart = h
		if parsed, err := strconv.Atoi(p); err == nil {
			port = parsed
		}
	} else if strings.Contains(relay, ":") {
		parts := strings.Split(relay, ":")
		hostPart = parts[0]
		if parsed, err := strconv.Atoi(parts[1]); err == nil {
			port = parsed
		}
	}

	identityPort := port
	libp2pPort := port
	if port == 4003 {
		libp2pPort = 4001
	}
	if port == 4001 {
		identityPort = 4003
	}

	peerID, err := fetchRelayIdentity(hostPart, identityPort)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/ip4/%s/tcp/%d/p2p/%s", hostPart, libp2pPort, peerID), nil
}

func fetchRelayIdentity(hostPart string, port int) (string, error) {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", hostPart, port), 10*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	writer := protocol.NewFrameWriter(conn)
	reader := protocol.NewFrameReader(conn)
	if err := writer.WriteFrame(&protocol.Frame{Type: protocol.MsgGetIdentity}); err != nil {
		return "", err
	}
	frame, err := reader.ReadFrame()
	if err != nil {
		return "", err
	}
	if frame.Type != protocol.MsgIdentityRes {
		return "", fmt.Errorf("unexpected identity response type: 0x%02x", frame.Type)
	}
	var identity protocol.IdentityResponsePayload
	if err := json.Unmarshal(frame.Payload, &identity); err != nil {
		return "", err
	}
	return identity.PeerID, nil
}

func lookupSeedsViaRelay(relayAddrStr string, fileHash string) ([]protocol.SeedInfo, error) {
	relayHost, libp2pPort, err := splitRelayMultiaddr(relayAddrStr)
	if err != nil {
		return nil, err
	}
	discoveryPort := libp2pPort + 2

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", relayHost, discoveryPort), 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial relay discovery: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

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
		_ = json.Unmarshal(frame.Payload, &ep)
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
	host, portStr, err := net.SplitHostPort(addr)
	if err == nil {
		port, _ := strconv.Atoi(portStr)
		return host, uint16(port)
	}
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		return addr, 4002
	}
	port, _ := strconv.Atoi(addr[idx+1:])
	return addr[:idx], uint16(port)
}

func isRoutableAddr(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}

func filterRoutableSeeds(seeds []protocol.SeedInfo) []protocol.SeedInfo {
	var out []protocol.SeedInfo
	for _, seed := range seeds {
		hostPart, _ := parseHostPort(seed.TransportAddr)
		if isRoutableAddr(hostPart) {
			out = append(out, seed)
		}
	}
	return out
}

func tryHolePunchedLibp2pStream(h host.Host, seedID peer.ID) (network.Stream, bool) {
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		for _, conn := range h.Network().ConnsToPeer(seedID) {
			if conn.Stat().Limited {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			stream, err := h.NewStream(ctx, seedID, libp2pProto.ID(protocol.MagnetProtocolID))
			cancel()
			if err == nil && !stream.Conn().Stat().Limited {
				return stream, true
			}
			if err == nil {
				stream.Close()
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil, false
}

func buildSeedAddresses(cfg *config.Config, natMappings []*magnetkNAT.Mapping) []string {
	var out []string
	seen := make(map[string]bool)
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	addPair := func(ip string, tcpPort, udpPort int) {
		if ip == "" || ip == "0.0.0.0" {
			return
		}
		add(fmt.Sprintf("/ip4/%s/tcp/%d", ip, tcpPort))
		add(fmt.Sprintf("/ip4/%s/udp/%d/quic-v1", ip, udpPort))
	}

	for _, ip := range magnetkNet.LANIPs() {
		addPair(ip, cfg.TransportPort, cfg.TransportPort)
	}
	for _, m := range natMappings {
		if m.ExternalIP == nil {
			continue
		}
		switch m.Protocol {
		case "tcp":
			add(fmt.Sprintf("/ip4/%s/tcp/%d", m.ExternalIP, m.ExternalPort))
		case "udp":
			add(fmt.Sprintf("/ip4/%s/udp/%d/quic-v1", m.ExternalIP, m.ExternalPort))
		}
	}
	if cfg.AdvertisedAddress != "" {
		addPair(cfg.AdvertisedAddress, cfg.TransportPort, cfg.TransportPort)
	} else if publicIP, _, err := getPublicIdentity("stun.l.google.com:19302"); err == nil {
		addPair(publicIP, cfg.TransportPort, cfg.TransportPort)
	}
	return out
}

func getPublicIdentity(stunServer string) (string, int, error) {
	conn, err := net.Dial("udp", stunServer)
	if err != nil {
		return "", 0, err
	}
	defer conn.Close()

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
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp := make([]byte, 1024)
	n, err := conn.Read(resp)
	if err != nil {
		return "", 0, err
	}

	offset := 20
	for offset < n-4 {
		attrType := uint16(resp[offset])<<8 | uint16(resp[offset+1])
		attrLen := uint16(resp[offset+2])<<8 | uint16(resp[offset+3])
		if attrType == 0x0020 {
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
	return "", 0, fmt.Errorf("XOR-MAPPED-ADDRESS attribute not found")
}

func sendHeartbeat(h host.Host, relayID peer.ID) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.NewStream(ctx, relayID, libp2pProto.ID(protocol.DiscoveryProtocolID))
	if err != nil {
		return err
	}
	defer stream.Close()

	msg, err := protocol.NewMessage(protocol.LegacyMsgHeartbeat, protocol.LegacyHeartbeatPayload{PeerID: h.ID().String()})
	if err != nil {
		return err
	}
	data, err := msg.Encode()
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(stream)
	if _, err := writer.Write(data); err != nil {
		return err
	}
	if err := writer.WriteByte('\n'); err != nil {
		return err
	}
	return writer.Flush()
}

func resolveOutputPath(outputDir, fileName string) (string, error) {
	if outputDir == "" {
		outputDir = "."
	}
	clean := filepath.Clean(outputDir)
	if filepath.Ext(clean) == "" {
		if err := os.MkdirAll(clean, 0755); err != nil {
			return "", err
		}
		return filepath.Join(clean, fileName), nil
	}
	if err := os.MkdirAll(filepath.Dir(clean), 0755); err != nil {
		return "", err
	}
	return clean, nil
}

func reportProgress(cb ProgressCallback, manifest protocol.ManifestResponsePayload, downloaded int64, doneChunks uint32, start time.Time, resumeBytes int64) {
	if cb == nil {
		return
	}
	if downloaded > manifest.FileSize {
		downloaded = manifest.FileSize
	}
	elapsed := time.Since(start).Seconds()
	speed := 0.0
	if elapsed > 0 {
		speed = float64(downloaded-resumeBytes) / elapsed / 1024 / 1024
	}
	eta := time.Duration(0)
	if speed > 0 {
		remaining := float64(manifest.FileSize-downloaded) / (speed * 1024 * 1024)
		eta = time.Duration(remaining) * time.Second
	}
	percent := 0.0
	if manifest.FileSize > 0 {
		percent = float64(downloaded) / float64(manifest.FileSize) * 100
	}
	cb(Progress{
		FileName:    manifest.FileName,
		FileSize:    manifest.FileSize,
		Downloaded:  downloaded,
		TotalChunks: manifest.TotalChunks,
		DoneChunks:  doneChunks,
		Percent:     percent,
		SpeedMBps:   speed,
		ETA:         eta,
	})
}
