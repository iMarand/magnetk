// Package protocol defines the binary framing format for Magnet P2P messages.
//
// Frame format:
//
//	[4 bytes: payload length (big-endian)] [1 byte: message type] [payload]
//
// Control messages (discovery, manifest, etc.) use JSON payloads.
// Chunk data uses raw binary payloads for zero overhead.
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Message type constants (uint8)
const (
	// Discovery messages (JSON payload)
	MsgRegisterPeer   uint8 = 0x01
	MsgPeerRegistered uint8 = 0x02
	MsgLookupPeer     uint8 = 0x03
	MsgLookupResult   uint8 = 0x04

	// File messages (JSON payload)
	MsgPublishFile   uint8 = 0x05
	MsgPublishAck    uint8 = 0x06
	MsgUnpublishFile uint8 = 0x07
	MsgUnpublishAck  uint8 = 0x08

	// Manifest messages (JSON payload)
	MsgManifestReq uint8 = 0x10
	MsgManifestRes uint8 = 0x11

	// Chunk messages (binary payload for data)
	MsgChunkReq uint8 = 0x20
	MsgChunkRes uint8 = 0x21

	// Heartbeat
	MsgHeartbeat    uint8 = 0x30
	MsgHeartbeatAck uint8 = 0x31

	// Identity
	MsgGetIdentity uint8 = 0x32
	MsgIdentityRes uint8 = 0x33

	// Proxy (relay bridges TCP client to libp2p seed)
	MsgProxyConnect uint8 = 0x40
	MsgProxyReady   uint8 = 0x41

	// Error
	MsgError uint8 = 0xFF
)

// MaxFrameSize is the maximum allowed frame size (64 MB)
const MaxFrameSize = 64 * 1024 * 1024

// Frame represents a single protocol frame.
type Frame struct {
	Type    uint8
	Payload []byte
}

// FrameWriter writes framed messages to an io.Writer.
type FrameWriter struct {
	w   io.Writer
	buf [5]byte // header buffer
}

// NewFrameWriter creates a new FrameWriter.
func NewFrameWriter(w io.Writer) *FrameWriter {
	return &FrameWriter{w: w}
}

// WriteFrame writes a complete frame.
func (fw *FrameWriter) WriteFrame(f *Frame) error {
	if len(f.Payload) > MaxFrameSize {
		return fmt.Errorf("frame too large: %d bytes", len(f.Payload))
	}

	// Write header: [4-byte length][1-byte type]
	binary.BigEndian.PutUint32(fw.buf[:4], uint32(len(f.Payload)))
	fw.buf[4] = f.Type

	if _, err := fw.w.Write(fw.buf[:]); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}

	if len(f.Payload) > 0 {
		if _, err := fw.w.Write(f.Payload); err != nil {
			return fmt.Errorf("write frame payload: %w", err)
		}
	}

	return nil
}

// WriteJSON writes a JSON-encoded frame.
func (fw *FrameWriter) WriteJSON(msgType uint8, v interface{}) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	return fw.WriteFrame(&Frame{Type: msgType, Payload: payload})
}

// WriteChunk writes a raw binary chunk frame.
func (fw *FrameWriter) WriteChunk(index uint32, data []byte) error {
	// Chunk payload: [4-byte index][raw data]
	payload := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(payload[:4], index)
	copy(payload[4:], data)
	return fw.WriteFrame(&Frame{Type: MsgChunkRes, Payload: payload})
}

// FrameReader reads framed messages from an io.Reader.
type FrameReader struct {
	r   io.Reader
	buf [5]byte
}

// NewFrameReader creates a new FrameReader.
func NewFrameReader(r io.Reader) *FrameReader {
	return &FrameReader{r: r}
}

// ReadFrame reads a complete frame. Returns io.EOF when stream ends.
func (fr *FrameReader) ReadFrame() (*Frame, error) {
	// Read header
	if _, err := io.ReadFull(fr.r, fr.buf[:]); err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint32(fr.buf[:4])
	msgType := fr.buf[4]

	if length > MaxFrameSize {
		return nil, fmt.Errorf("frame too large: %d bytes", length)
	}

	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(fr.r, payload); err != nil {
			return nil, fmt.Errorf("read frame payload: %w", err)
		}
	}

	return &Frame{Type: msgType, Payload: payload}, nil
}

// ReadFrameWithProgress reads a complete frame and calls onPayloadBytes as the
// payload arrives. It is useful for large chunk frames over slower relayed
// paths where a plain ReadFrame would look stuck until the whole chunk lands.
func (fr *FrameReader) ReadFrameWithProgress(onPayloadBytes func(read, total uint32)) (*Frame, error) {
	if _, err := io.ReadFull(fr.r, fr.buf[:]); err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint32(fr.buf[:4])
	msgType := fr.buf[4]

	if length > MaxFrameSize {
		return nil, fmt.Errorf("frame too large: %d bytes", length)
	}

	payload := make([]byte, length)
	var read uint32
	for read < length {
		n, err := fr.r.Read(payload[read:])
		if n > 0 {
			read += uint32(n)
			if onPayloadBytes != nil {
				onPayloadBytes(read, length)
			}
		}
		if err != nil {
			if err == io.EOF && read == length {
				break
			}
			return nil, fmt.Errorf("read frame payload: %w", err)
		}
		if n == 0 {
			return nil, io.ErrUnexpectedEOF
		}
	}

	return &Frame{Type: msgType, Payload: payload}, nil
}

// ReadJSON reads a frame and decodes the JSON payload.
func (fr *FrameReader) ReadJSON(expectedType uint8, v interface{}) error {
	f, err := fr.ReadFrame()
	if err != nil {
		return err
	}
	if f.Type != expectedType {
		return fmt.Errorf("unexpected message type: got 0x%02x, want 0x%02x", f.Type, expectedType)
	}
	if err := json.Unmarshal(f.Payload, v); err != nil {
		return fmt.Errorf("unmarshal JSON (type 0x%02x): %w", f.Type, err)
	}
	return nil
}

// ParseChunkPayload extracts the chunk index and raw data from a chunk frame.
func ParseChunkPayload(payload []byte) (index uint32, data []byte, err error) {
	if len(payload) < 4 {
		return 0, nil, fmt.Errorf("chunk payload too short: %d bytes", len(payload))
	}
	index = binary.BigEndian.Uint32(payload[:4])
	data = payload[4:]
	return index, data, nil
}

// ===== Payload Types (JSON) =====

// PeerRegisterPayload is sent by seeds/peers to register with relay.
type PeerRegisterPayload struct {
	PeerID    string   `json:"peer_id"`
	Addresses []string `json:"addresses"`
	Role      string   `json:"role"` // "seed" or "peer"
}

// PeerRegisteredPayload is the relay's response to registration.
type PeerRegisteredPayload struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// LookupPeerPayload requests seeds for a file.
type LookupPeerPayload struct {
	FileHash string `json:"file_hash"`
}

// LookupResultPayload returns matching seeds.
type LookupResultPayload struct {
	Found bool       `json:"found"`
	Seeds []SeedInfo `json:"seeds"`
}

// SeedInfo describes a seed that has a file.
type SeedInfo struct {
	PeerID        string   `json:"peer_id"`
	Addresses     []string `json:"addresses"`
	TransportAddr string   `json:"transport_addr"` // MsQuic address (host:port)
}

// PublishFilePayload registers a file with the relay.
type PublishFilePayload struct {
	FileHash      string `json:"file_hash"`
	FileName      string `json:"file_name"`
	FileSize      int64  `json:"file_size"`
	ChunkSize     int64  `json:"chunk_size"`
	PeerID        string `json:"peer_id"`
	TransportAddr string `json:"transport_addr"`
}

// PublishAckPayload confirms file publication.
type PublishAckPayload struct {
	Success   bool   `json:"success"`
	MagnetURI string `json:"magnet_uri,omitempty"`
}

// ManifestRequestPayload requests a file manifest.
type ManifestRequestPayload struct {
	FileHash string `json:"file_hash"`
}

// ManifestResponsePayload returns the file manifest.
type ManifestResponsePayload struct {
	FileHash    string      `json:"file_hash"`
	FileName    string      `json:"file_name"`
	FileSize    int64       `json:"file_size"`
	ChunkSize   int64       `json:"chunk_size"`
	TotalChunks uint32      `json:"total_chunks"`
	Chunks      []ChunkMeta `json:"chunks"`
}

// ChunkMeta describes a single chunk in the manifest.
type ChunkMeta struct {
	Index  uint32 `json:"index"`
	Hash   string `json:"hash"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
}

// ChunkRequestPayload requests specific chunks.
type ChunkRequestPayload struct {
	FileHash     string   `json:"file_hash"`
	ChunkIndices []uint32 `json:"chunk_indices"`
}

// ErrorPayload represents an error message.
type ErrorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// HeartbeatPayload is a keep-alive message.
type HeartbeatPayload struct {
	PeerID    string `json:"peer_id"`
	Timestamp int64  `json:"timestamp"`
}

// IdentityResponsePayload contains the relay's identity.
type IdentityResponsePayload struct {
	PeerID     string   `json:"peer_id"`
	Multiaddrs []string `json:"multiaddrs"`
}

// ProxyConnectPayload requests the relay to proxy a connection to a seed.
type ProxyConnectPayload struct {
	PeerID   string `json:"peer_id"`
	FileHash string `json:"file_hash"`
}

// ProxyReadyPayload confirms the relay has connected to the seed.
type ProxyReadyPayload struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}
