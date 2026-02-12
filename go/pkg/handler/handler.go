// Package handler implements protocol handlers for Magnet P2P
package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"magnetk/pkg/buffer"
	"magnetk/pkg/chunk"
	"magnetk/pkg/protocol"
	"magnetk/pkg/registry"
	"magnetk/pkg/types"
)

// RelayHandler handles protocol messages for the relay node
type RelayHandler struct {
	host     host.Host
	registry *registry.FileRegistry
	buffer   *buffer.ChunkBuffer
	peers    map[string]*types.PeerInfo
}

// NewRelayHandler creates a new RelayHandler
func NewRelayHandler(h host.Host, reg *registry.FileRegistry, buf *buffer.ChunkBuffer) *RelayHandler {
	rh := &RelayHandler{
		host:     h,
		registry: reg,
		buffer:   buf,
		peers:    make(map[string]*types.PeerInfo),
	}

	// Register stream handlers
	h.SetStreamHandler(protocol.DiscoveryProtocolID, rh.handleDiscovery)
	h.SetStreamHandler(protocol.MagnetProtocolID, rh.handleTransfer)

	return rh
}

// handleDiscovery handles discovery protocol messages
func (rh *RelayHandler) handleDiscovery(s network.Stream) {
	defer s.Close()

	reader := bufio.NewReader(s)
	writer := bufio.NewWriter(s)

	// Read message
	data, err := reader.ReadBytes('\n')
	if err != nil {
		fmt.Printf("Error reading discovery message: %v\n", err)
		return
	}

	msg, err := protocol.DecodeMessage(data)
	if err != nil {
		fmt.Printf("Error decoding message: %v\n", err)
		return
	}

	var response *protocol.Message

	switch msg.Type {
	case protocol.LegacyMsgRegisterPeer:
		response = rh.handleRegisterPeer(msg, s.Conn().RemotePeer())

	case protocol.LegacyMsgLookupPeer:
		response = rh.handleLookupPeer(msg)

	case protocol.LegacyMsgPublishFile:
		response = rh.handlePublishFile(msg)

	case protocol.LegacyMsgHeartbeat:
		response = rh.handleHeartbeat(msg)

	default:
		response, _ = protocol.NewMessage(protocol.LegacyMsgError, protocol.LegacyErrorPayload{
			Code:    protocol.ErrCodeInvalidRequest,
			Message: "Unknown message type",
		})
	}

	if response != nil {
		data, _ := response.Encode()
		writer.Write(data)
		writer.WriteByte('\n')
		writer.Flush()
	}
}

// HandleTCPDiscovery handles discovery protocol over raw TCP (fallback for JS SDK)
func (rh *RelayHandler) HandleTCPDiscovery(conn net.Conn) {
	defer conn.Close()

	reader := protocol.NewFrameReader(conn)
	writer := protocol.NewFrameWriter(conn)

	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			if err != io.EOF {
				fmt.Printf("[relay] TCP discovery error: %v\n", err)
			}
			return
		}

		switch frame.Type {
		case protocol.MsgLookupPeer:
			var payload protocol.LookupPeerPayload
			if err := json.Unmarshal(frame.Payload, &payload); err != nil {
				writer.WriteJSON(protocol.MsgError, protocol.ErrorPayload{
					Code:    protocol.ErrCodeInvalidRequest,
					Message: "Invalid payload",
				})
				continue
			}
			rh.handleTCPMsgLookupPeer(payload, writer)

		case protocol.MsgHeartbeat:
			// Heartbeat for TCP clients
			writer.WriteJSON(protocol.MsgHeartbeatAck, protocol.HeartbeatPayload{Timestamp: time.Now().Unix()})

		case protocol.MsgGetIdentity:
			// Get relay identity (PeerID + Multiaddrs)
			addrs := rh.host.Addrs()
			addrStrings := make([]string, len(addrs))
			for i, addr := range addrs {
				addrStrings[i] = fmt.Sprintf("%s/p2p/%s", addr.String(), rh.host.ID().String())
			}
			writer.WriteJSON(protocol.MsgIdentityRes, protocol.IdentityResponsePayload{
				PeerID:     rh.host.ID().String(),
				Multiaddrs: addrStrings,
			})

		default:
			writer.WriteJSON(protocol.MsgError, protocol.ErrorPayload{
				Code:    protocol.ErrCodeInvalidRequest,
				Message: "Unsupported message type in TCP fallback",
			})
		}
	}
}

func (rh *RelayHandler) handleTCPMsgLookupPeer(payload protocol.LookupPeerPayload, writer *protocol.FrameWriter) {
	entry, found := rh.registry.LookupFile(payload.FileHash)
	if !found {
		writer.WriteJSON(protocol.MsgLookupResult, protocol.LookupResultPayload{
			Seeds: []protocol.SeedInfo{},
		})
		return
	}

	seeds := make([]protocol.SeedInfo, 0, len(entry.Seeds))
	for _, s := range entry.Seeds {
		// Try to find the MsQuic address in s.Addresses
		transportAddr := ""
		for _, addr := range s.Addresses {
			if strings.Contains(addr, "/quic-v1") {
				parts := strings.Split(addr, "/")
				if len(parts) >= 5 {
					transportAddr = fmt.Sprintf("%s:%s", parts[2], parts[4])
				}
				break
			}
		}

		seeds = append(seeds, protocol.SeedInfo{
			PeerID:        s.PeerID,
			Addresses:     s.Addresses,
			TransportAddr: transportAddr,
		})
	}

	writer.WriteJSON(protocol.MsgLookupResult, protocol.LookupResultPayload{
		Found: true,
		Seeds: seeds,
	})
}

func (rh *RelayHandler) handleRegisterPeer(msg *protocol.Message, peerID peer.ID) *protocol.Message {
	var payload protocol.LegacyRegisterPeerPayload
	if err := msg.ParsePayload(&payload); err != nil {
		// RegisterPeerPayload was NOT renamed in messages.go because it didn't conflict.
		// But MsgError WAS.
		resp, _ := protocol.NewMessage(protocol.LegacyMsgError, protocol.LegacyErrorPayload{
			Code:    protocol.ErrCodeInvalidRequest,
			Message: "Invalid payload",
		})
		return resp
	}

	// Register peer
	peerInfo := &types.PeerInfo{
		PeerID:    peerID.String(),
		Addresses: payload.Addresses,
		LastSeen:  time.Now(),
		Online:    true,
	}
	rh.peers[peerID.String()] = peerInfo

	// Check for buffered chunks and forward them
	go rh.forwardBufferedChunks(peerID.String())

	resp, _ := protocol.NewMessage(protocol.LegacyMsgRegisterAck, protocol.LegacyRegisterAckPayload{
		Success: true,
		TTL:     300, // 5 minutes
		Message: "Registered successfully",
	})
	return resp
}

func (rh *RelayHandler) handleLookupPeer(msg *protocol.Message) *protocol.Message {
	var payload protocol.LegacyLookupPeerPayload
	if err := msg.ParsePayload(&payload); err != nil {
		// RegisterPeerPayload was NOT renamed in messages.go because it didn't conflict.
		// But MsgError WAS.
		resp, _ := protocol.NewMessage(protocol.LegacyMsgError, protocol.LegacyErrorPayload{
			Code:    protocol.ErrCodeInvalidRequest,
			Message: "Invalid payload",
		})
		return resp
	}

	// Lookup by file hash
	if payload.FileHash != "" {
		entry, found := rh.registry.LookupFile(payload.FileHash)
		if !found {
			resp, _ := protocol.NewMessage(protocol.LegacyMsgLookupResult, protocol.LegacyLookupResultPayload{
				Found: false,
			})
			return resp
		}

		resp, _ := protocol.NewMessage(protocol.LegacyMsgLookupResult, protocol.LegacyLookupResultPayload{
			Found: true,
			Seeds: entry.Seeds,
		})
		return resp
	}

	// Lookup by peer ID
	if payload.PeerID != "" {
		peerInfo, exists := rh.peers[payload.PeerID]
		if !exists || !peerInfo.Online {
			resp, _ := protocol.NewMessage(protocol.LegacyMsgLookupResult, protocol.LegacyLookupResultPayload{
				Found: false,
			})
			return resp
		}

		resp, _ := protocol.NewMessage(protocol.LegacyMsgLookupResult, protocol.LegacyLookupResultPayload{
			Found:     true,
			PeerID:    peerInfo.PeerID,
			Addresses: peerInfo.Addresses,
		})
		return resp
	}

	resp, _ := protocol.NewMessage(protocol.LegacyMsgError, protocol.LegacyErrorPayload{
		Code:    protocol.ErrCodeInvalidRequest,
		Message: "Must specify peer_id or file_hash",
	})
	return resp
}

func (rh *RelayHandler) handlePublishFile(msg *protocol.Message) *protocol.Message {
	var payload protocol.LegacyPublishFilePayload
	if err := msg.ParsePayload(&payload); err != nil {
		resp, _ := protocol.NewMessage(protocol.LegacyMsgError, protocol.LegacyErrorPayload{
			Code:    protocol.ErrCodeInvalidRequest,
			Message: "Invalid payload",
		})
		return resp
	}

	// Get peer info
	peerInfo, exists := rh.peers[payload.PeerID]
	if !exists {
		peerInfo = &types.PeerInfo{
			PeerID:    payload.PeerID,
			Addresses: payload.Addresses,
			LastSeen:  time.Now(),
			Online:    true,
		}
		rh.peers[payload.PeerID] = peerInfo
	} else if len(payload.Addresses) > 0 {
		// Update addresses if provided
		peerInfo.Addresses = payload.Addresses
		peerInfo.LastSeen = time.Now()
		peerInfo.Online = true
	}

	// Publish file
	magnetLink := rh.registry.PublishFile(
		payload.FileHash,
		payload.FileName,
		payload.FileSize,
		payload.ChunkCount,
		*peerInfo,
	)

	resp, _ := protocol.NewMessage(protocol.LegacyMsgPublishAck, protocol.LegacyPublishAckPayload{
		Success:    true,
		MagnetLink: magnetLink,
	})
	return resp
}

func (rh *RelayHandler) handleHeartbeat(msg *protocol.Message) *protocol.Message {
	var payload protocol.LegacyHeartbeatPayload
	if err := msg.ParsePayload(&payload); err != nil {
		resp, _ := protocol.NewMessage(protocol.LegacyMsgError, protocol.LegacyErrorPayload{
			Code:    protocol.ErrCodeInvalidRequest,
			Message: "Invalid payload",
		})
		return resp
	}

	// Update peer status
	if peerInfo, exists := rh.peers[payload.PeerID]; exists {
		peerInfo.LastSeen = time.Now()
		peerInfo.Online = true
		rh.registry.UpdateSeedStatus(payload.PeerID, true)
	}

	resp, _ := protocol.NewMessage(protocol.LegacyMsgHeartbeatAck, protocol.LegacyHeartbeatAckPayload{
		Ok: true,
	})
	return resp
}

func (rh *RelayHandler) handleTransfer(s network.Stream) {
	defer s.Close()

	reader := bufio.NewReader(s)
	writer := bufio.NewWriter(s)

	// Read message
	data, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}

	msg, err := protocol.DecodeMessage(data)
	if err != nil {
		return
	}

	switch msg.Type {
	case protocol.LegacyMsgChunkRes:
		// Buffer chunk for offline peer
		var payload protocol.LegacyChunkResponsePayload
		if err := msg.ParsePayload(&payload); err != nil {
			return
		}

		// Store in buffer (placeholder for future routing)
		_ = &types.Chunk{
			Index: payload.Index,
			Hash:  payload.Hash,
			Data:  payload.Data,
		}

		// TODO: Determine target peer from context
		// For now, direct P2P routing is used between seeds and peers
	}

	writer.Flush()
}

func (rh *RelayHandler) forwardBufferedChunks(peerID string) {
	chunks := rh.buffer.GetChunksForPeer(peerID)
	if len(chunks) == 0 {
		return
	}

	// Get peer connection
	pid, err := peer.Decode(peerID)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := rh.host.NewStream(ctx, pid, protocol.MagnetProtocolID)
	if err != nil {
		return
	}
	defer s.Close()

	writer := bufio.NewWriter(s)

	for _, bc := range chunks {
		msg, _ := protocol.NewMessage(protocol.LegacyMsgChunkRes, protocol.LegacyChunkResponsePayload{
			FileID: bc.FileID,
			Index:  bc.Chunk.Index,
			Hash:   bc.Chunk.Hash,
			Data:   bc.Chunk.Data,
		})

		data, _ := msg.Encode()
		writer.Write(data)
		writer.WriteByte('\n')
		writer.Flush()

		// Remove from buffer after forwarding
		rh.buffer.RemoveChunk(peerID, bc.FileID, bc.Chunk.Index)
	}

	fmt.Printf("Forwarded %d buffered chunks to peer %s\n", len(chunks), peerID[:12])
}

// SeedHandler handles protocol messages for seed nodes
type SeedHandler struct {
	host         host.Host
	chunkManager *chunk.Manager
	files        map[string]string // fileID -> filePath
}

// NewSeedHandler creates a new SeedHandler
func NewSeedHandler(h host.Host, chunkSize int64) *SeedHandler {
	sh := &SeedHandler{
		host:         h,
		chunkManager: chunk.NewManager(chunkSize),
		files:        make(map[string]string),
	}

	h.SetStreamHandler(protocol.MagnetProtocolID, sh.handleTransfer)

	return sh
}

// RegisterFile registers a file for serving
func (sh *SeedHandler) RegisterFile(fileID, filePath string) {
	sh.files[fileID] = filePath
}

func (sh *SeedHandler) handleTransfer(s network.Stream) {
	defer s.Close()

	reader := bufio.NewReader(s)
	writer := bufio.NewWriter(s)

	for {
		data, err := reader.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				fmt.Printf("Error reading: %v\n", err)
			}
			return
		}

		msg, err := protocol.DecodeMessage(data)
		if err != nil {
			continue
		}

		var response *protocol.Message

		switch msg.Type {
		case protocol.LegacyMsgManifestReq:
			response = sh.handleManifestRequest(msg)

		case protocol.LegacyMsgChunkReq:
			sh.handleChunkRequest(msg, writer)
			continue // Multiple responses

		case protocol.LegacyMsgResumeReq:
			sh.handleResumeRequest(msg, writer)
			continue
		}

		if response != nil {
			respData, _ := response.Encode()
			writer.Write(respData)
			writer.WriteByte('\n')
			writer.Flush()
		}
	}
}

func (sh *SeedHandler) handleManifestRequest(msg *protocol.Message) *protocol.Message {
	var payload protocol.LegacyManifestRequestPayload
	if err := msg.ParsePayload(&payload); err != nil {
		resp, _ := protocol.NewMessage(protocol.LegacyMsgError, protocol.LegacyErrorPayload{
			Code:    protocol.ErrCodeInvalidRequest,
			Message: "Invalid payload",
		})
		return resp
	}

	filePath, exists := sh.files[payload.FileID]
	if !exists {
		resp, _ := protocol.NewMessage(protocol.LegacyMsgError, protocol.LegacyErrorPayload{
			Code:    protocol.ErrCodeFileNotFound,
			Message: "File not found",
		})
		return resp
	}

	manifest, err := sh.chunkManager.GenerateManifest(filePath)
	if err != nil {
		resp, _ := protocol.NewMessage(protocol.LegacyMsgError, protocol.LegacyErrorPayload{
			Code:    protocol.ErrCodeFileNotFound,
			Message: err.Error(),
		})
		return resp
	}

	resp, _ := protocol.NewMessage(protocol.LegacyMsgManifestRes, protocol.LegacyManifestResponsePayload{
		Manifest: manifest,
	})
	return resp
}

func (sh *SeedHandler) handleChunkRequest(msg *protocol.Message, writer *bufio.Writer) {
	var payload protocol.LegacyChunkRequestPayload
	if err := msg.ParsePayload(&payload); err != nil {
		return
	}

	filePath, exists := sh.files[payload.FileID]
	if !exists {
		return
	}

	for _, idx := range payload.ChunkIndices {
		c, err := sh.chunkManager.ReadChunk(filePath, idx)
		if err != nil {
			continue
		}

		resp, _ := protocol.NewMessage(protocol.LegacyMsgChunkRes, protocol.LegacyChunkResponsePayload{
			FileID: payload.FileID,
			Index:  c.Index,
			Hash:   c.Hash,
			Data:   c.Data,
		})

		data, _ := resp.Encode()
		writer.Write(data)
		writer.WriteByte('\n')
		writer.Flush()

		// Small delay to avoid overwhelming
		time.Sleep(10 * time.Millisecond)
	}
}

func (sh *SeedHandler) handleResumeRequest(msg *protocol.Message, writer *bufio.Writer) {
	var payload protocol.LegacyResumeRequestPayload
	if err := msg.ParsePayload(&payload); err != nil {
		return
	}

	filePath, exists := sh.files[payload.FileID]
	if !exists {
		return
	}

	// Get manifest to know total chunks
	manifest, err := sh.chunkManager.GenerateManifest(filePath)
	if err != nil {
		return
	}

	// Build set of received chunks
	received := make(map[uint32]bool)
	for _, idx := range payload.ReceivedChunks {
		received[idx] = true
	}

	// Send missing chunks
	for i := uint32(0); i < manifest.TotalChunks; i++ {
		if received[i] {
			continue
		}

		c, err := sh.chunkManager.ReadChunk(filePath, i)
		if err != nil {
			continue
		}

		resp, _ := protocol.NewMessage(protocol.LegacyMsgChunkRes, protocol.LegacyChunkResponsePayload{
			FileID: payload.FileID,
			Index:  c.Index,
			Hash:   c.Hash,
			Data:   c.Data,
		})

		data, _ := resp.Encode()
		writer.Write(data)
		writer.WriteByte('\n')
		writer.Flush()

		time.Sleep(10 * time.Millisecond)
	}
}

// PeerClient handles downloading files from seeds
type PeerClient struct {
	host         host.Host
	chunkManager *chunk.Manager
}

// NewPeerClient creates a new PeerClient
func NewPeerClient(h host.Host, chunkSize int64) *PeerClient {
	return &PeerClient{
		host:         h,
		chunkManager: chunk.NewManager(chunkSize),
	}
}

// RequestManifest requests a file manifest from a seed
func (pc *PeerClient) RequestManifest(ctx context.Context, seedID peer.ID, fileID string) (*types.FileManifest, error) {
	s, err := pc.host.NewStream(ctx, seedID, protocol.MagnetProtocolID)
	if err != nil {
		return nil, fmt.Errorf("failed to open stream: %w", err)
	}
	defer s.Close()

	reader := bufio.NewReader(s)
	writer := bufio.NewWriter(s)

	// Send request
	msg, _ := protocol.NewMessage(protocol.LegacyMsgManifestReq, protocol.LegacyManifestRequestPayload{
		FileID: fileID,
	})
	data, _ := msg.Encode()
	writer.Write(data)
	writer.WriteByte('\n')
	writer.Flush()

	// Read response
	respData, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	resp, err := protocol.DecodeMessage(respData)
	if err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if resp.Type == protocol.LegacyMsgError {
		var errPayload protocol.LegacyErrorPayload
		resp.ParsePayload(&errPayload)
		return nil, fmt.Errorf("error: %s", errPayload.Message)
	}

	var payload protocol.LegacyManifestResponsePayload
	if err := resp.ParsePayload(&payload); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	return payload.Manifest, nil
}

// RequestChunks requests a batch of chunks from a seed
func (pc *PeerClient) RequestChunks(ctx context.Context, seedID peer.ID, fileID string, indices []uint32) ([]*types.Chunk, error) {
	s, err := pc.host.NewStream(ctx, seedID, protocol.MagnetProtocolID)
	if err != nil {
		return nil, fmt.Errorf("failed to open stream: %w", err)
	}
	defer s.Close()

	reader := bufio.NewReader(s)
	writer := bufio.NewWriter(s)

	// Send request
	msg, _ := protocol.NewMessage(protocol.LegacyMsgChunkReq, protocol.LegacyChunkRequestPayload{
		FileID:       fileID,
		ChunkIndices: indices,
	})
	data, _ := msg.Encode()
	writer.Write(data)
	writer.WriteByte('\n')
	writer.Flush()

	// Read responses
	chunks := make([]*types.Chunk, 0, len(indices))

	for i := 0; i < len(indices); i++ {
		respData, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return chunks, fmt.Errorf("failed to read chunk: %w", err)
		}

		resp, err := protocol.DecodeMessage(respData)
		if err != nil {
			continue
		}

		if resp.Type == protocol.LegacyMsgChunkRes {
			var payload protocol.LegacyChunkResponsePayload
			if err := json.Unmarshal(resp.Payload, &payload); err != nil {
				continue
			}

			chunks = append(chunks, &types.Chunk{
				Index: payload.Index,
				Hash:  payload.Hash,
				Data:  payload.Data,
			})
		}
	}

	return chunks, nil
}
