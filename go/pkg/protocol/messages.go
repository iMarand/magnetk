// Package protocol defines the message types and handlers for Magnet P2P protocol
package protocol

import (
	"encoding/json"
	"fmt"
	"magnet/pkg/types"
)

// Protocol IDs for libp2p streams
const (
	// MagnetProtocolID is the main protocol for file transfer
	MagnetProtocolID = "/magnet/transfer/1.0.0"
	// DiscoveryProtocolID is for peer discovery and relay communication
	DiscoveryProtocolID = "/magnet/discovery/1.0.0"
)

// MessageType identifies the type of protocol message
type MessageType string

const (
	// Discovery messages (Peer <-> Relay)
	LegacyMsgRegisterPeer MessageType = "REGISTER_PEER"
	LegacyMsgRegisterAck  MessageType = "REGISTER_ACK"
	LegacyMsgLookupPeer   MessageType = "LOOKUP_PEER"
	LegacyMsgLookupResult MessageType = "LOOKUP_RESULT"
	LegacyMsgHeartbeat    MessageType = "HEARTBEAT"
	LegacyMsgHeartbeatAck MessageType = "HEARTBEAT_ACK"

	// File publishing (Seed -> Relay)
	LegacyMsgPublishFile   MessageType = "PUBLISH_FILE"
	LegacyMsgPublishAck    MessageType = "PUBLISH_ACK"
	LegacyMsgUnpublishFile MessageType = "UNPUBLISH_FILE"

	// File transfer messages (Peer <-> Seed)
	LegacyMsgManifestReq  MessageType = "MANIFEST_REQUEST"
	LegacyMsgManifestRes  MessageType = "MANIFEST_RESPONSE"
	LegacyMsgChunkReq     MessageType = "CHUNK_REQUEST"
	LegacyMsgChunkRes     MessageType = "CHUNK_RESPONSE"
	LegacyMsgResumeReq    MessageType = "RESUME_REQUEST"
	LegacyMsgTransferDone MessageType = "TRANSFER_COMPLETE"

	// Error message
	LegacyMsgError MessageType = "ERROR"
)

// Error codes for LegacyErrorPayload
const (
	ErrCodeInvalidRequest = 400
	ErrCodeFileNotFound   = 404
	ErrCodeInternal       = 500
)

// Message is the base structure for all protocol messages
type Message struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// NewMessage creates a new Message with the given type and payload
func NewMessage(msgType MessageType, payload interface{}) (*Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}
	return &Message{
		Type:    msgType,
		Payload: data,
	}, nil
}

// ParsePayload unmarshals the message payload into the provided struct
func (m *Message) ParsePayload(v interface{}) error {
	return json.Unmarshal(m.Payload, v)
}

// Encode serializes the message to JSON bytes
func (m *Message) Encode() ([]byte, error) {
	return json.Marshal(m)
}

// DecodeMessage deserializes JSON bytes into a Message
func DecodeMessage(data []byte) (*Message, error) {
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("failed to decode message: %w", err)
	}
	return &msg, nil
}

// ==================== Payload Structures ====================
// Prefixing ALL with Legacy to avoid conflicts with framing.go

// LegacyRegisterPeerPayload is sent when a peer registers with a relay
type LegacyRegisterPeerPayload struct {
	PeerID    string     `json:"peer_id"`
	Addresses []string   `json:"addresses"`
	Files     []FileInfo `json:"files,omitempty"` // For seeds publishing files
}

// FileInfo is basic file information for registration
type FileInfo struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}

// LegacyRegisterAckPayload is the response to peer registration
type LegacyRegisterAckPayload struct {
	Success bool   `json:"success"`
	TTL     int    `json:"ttl"` // Time-to-live in seconds, needs re-register before expiry
	Message string `json:"message,omitempty"`
}

// LegacyLookupPeerPayload is sent to find a peer or file
type LegacyLookupPeerPayload struct {
	PeerID   string `json:"peer_id,omitempty"`
	FileHash string `json:"file_hash,omitempty"`
}

// LegacyLookupResultPayload contains the result of a peer/file lookup
type LegacyLookupResultPayload struct {
	Found     bool             `json:"found"`
	PeerID    string           `json:"peer_id,omitempty"`
	Addresses []string         `json:"addresses,omitempty"`
	Seeds     []types.PeerInfo `json:"seeds,omitempty"` // Multiple seeds for a file
}

// LegacyHeartbeatPayload is sent periodically to indicate liveness
type LegacyHeartbeatPayload struct {
	PeerID string `json:"peer_id"`
}

// LegacyHeartbeatAckPayload acknowledges a heartbeat
type LegacyHeartbeatAckPayload struct {
	Ok bool `json:"ok"`
}

// LegacyPublishFilePayload is sent by seeds to publish a file
type LegacyPublishFilePayload struct {
	PeerID     string   `json:"peer_id"`
	Addresses  []string `json:"addresses"` // Added for discovery
	FileHash   string   `json:"file_hash"`
	FileName   string   `json:"file_name"`
	FileSize   int64    `json:"file_size"`
	ChunkCount uint32   `json:"chunk_count"`
}

type LegacyPublishAckPayload struct {
	Success    bool   `json:"success"`
	MagnetLink string `json:"magnet_link,omitempty"`
	Message    string `json:"message,omitempty"`
}

type LegacyManifestRequestPayload struct {
	FileID string `json:"file_id"`
}

type LegacyManifestResponsePayload struct {
	Manifest *types.FileManifest `json:"manifest"`
}

type LegacyChunkRequestPayload struct {
	FileID       string   `json:"file_id"`
	ChunkIndices []uint32 `json:"chunk_indices"` // Batch of chunk indices
}

type LegacyChunkResponsePayload struct {
	FileID string `json:"file_id"`
	Index  uint32 `json:"index"`
	Hash   string `json:"hash"`
	Data   []byte `json:"data"` // Base64 encoded in JSON
}

type LegacyResumeRequestPayload struct {
	FileID         string   `json:"file_id"`
	ReceivedChunks []uint32 `json:"received_chunks"` // Chunks already received
}

type LegacyTransferCompletePayload struct {
	FileID  string `json:"file_id"`
	Success bool   `json:"success"`
	Hash    string `json:"hash,omitempty"` // Verified file hash
}

type LegacyErrorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
