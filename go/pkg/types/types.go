// Package types defines common types used throughout the Magnet P2P system
package types

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// ChunkSize is the default chunk size in bytes (1 MB)
const DefaultChunkSize = 1 * 1024 * 1024 // 1 MB

// MaxChunkSize is the maximum allowed chunk size
const MaxChunkSize = 5 * 1024 * 1024 // 5 MB

// ChunkInfo contains metadata about a single chunk
type ChunkInfo struct {
	Index    uint32 `json:"index"`
	Size     uint32 `json:"size"`
	Hash     string `json:"hash"` // SHA-256 hex string
	Offset   int64  `json:"offset"`
	Received bool   `json:"received,omitempty"`
}

// FileManifest contains complete file metadata and chunk information
type FileManifest struct {
	FileID      string      `json:"file_id"` // SHA-256 of file content
	FileName    string      `json:"file_name"`
	FileSize    int64       `json:"file_size"`
	FileHash    string      `json:"file_hash"` // SHA-256 of complete file
	ChunkSize   uint32      `json:"chunk_size"`
	TotalChunks uint32      `json:"total_chunks"`
	Chunks      []ChunkInfo `json:"chunks"`
	CreatedAt   time.Time   `json:"created_at"`
}

// NewFileManifest creates a new FileManifest with calculated chunk count
func NewFileManifest(fileName string, fileSize int64, chunkSize uint32) *FileManifest {
	totalChunks := uint32((fileSize + int64(chunkSize) - 1) / int64(chunkSize))
	return &FileManifest{
		FileName:    fileName,
		FileSize:    fileSize,
		ChunkSize:   chunkSize,
		TotalChunks: totalChunks,
		Chunks:      make([]ChunkInfo, totalChunks),
		CreatedAt:   time.Now(),
	}
}

// TransferState tracks the progress of an ongoing transfer
type TransferState struct {
	Version        int           `json:"version"`
	TransferID     string        `json:"transfer_id"`
	OutputPath     string        `json:"output_path"`
	SenderPeerID   string        `json:"sender_peer_id"`
	Manifest       *FileManifest `json:"manifest"`
	ReceivedChunks []uint32      `json:"received_chunks"` // List of received chunk indices
	LastUpdated    time.Time     `json:"last_updated"`
	CreatedAt      time.Time     `json:"created_at"`

	// Convenience fields for quick lookup (duplicated from Manifest)
	FileHash        string   `json:"file_hash,omitempty"`
	FileName        string   `json:"file_name,omitempty"`
	FileSize        int64    `json:"file_size,omitempty"`
	TotalChunks     uint32   `json:"total_chunks,omitempty"`
	CompletedChunks []uint32 `json:"completed_chunks,omitempty"` // alias for ReceivedChunks
}

// NewTransferState creates a new TransferState
func NewTransferState(transferID, outputPath string, senderPeerID peer.ID, manifest *FileManifest) *TransferState {
	return &TransferState{
		Version:        1,
		TransferID:     transferID,
		OutputPath:     outputPath,
		SenderPeerID:   senderPeerID.String(),
		Manifest:       manifest,
		ReceivedChunks: make([]uint32, 0),
		LastUpdated:    time.Now(),
		CreatedAt:      time.Now(),
	}
}

// AddReceivedChunk marks a chunk as received
func (ts *TransferState) AddReceivedChunk(index uint32) {
	// Check if already received
	for _, idx := range ts.ReceivedChunks {
		if idx == index {
			return
		}
	}
	ts.ReceivedChunks = append(ts.ReceivedChunks, index)
	ts.LastUpdated = time.Now()
}

// GetMissingChunks returns indices of chunks not yet received
func (ts *TransferState) GetMissingChunks() []uint32 {
	if ts.Manifest == nil {
		return nil
	}

	received := make(map[uint32]bool)
	for _, idx := range ts.ReceivedChunks {
		received[idx] = true
	}

	missing := make([]uint32, 0)
	for i := uint32(0); i < ts.Manifest.TotalChunks; i++ {
		if !received[i] {
			missing = append(missing, i)
		}
	}
	return missing
}

// Progress returns the download progress as a percentage
func (ts *TransferState) Progress() float64 {
	if ts.Manifest == nil || ts.Manifest.TotalChunks == 0 {
		return 0
	}
	return float64(len(ts.ReceivedChunks)) / float64(ts.Manifest.TotalChunks) * 100
}

// PeerInfo contains information about a peer in the network
type PeerInfo struct {
	PeerID    string    `json:"peer_id"`
	Addresses []string  `json:"addresses"`
	LastSeen  time.Time `json:"last_seen"`
	Online    bool      `json:"online"`
	Bandwidth string    `json:"bandwidth,omitempty"` // e.g., "100mbps"
}

// FileRegistry entry for a file in the relay
type FileEntry struct {
	FileHash   string     `json:"file_hash"`
	FileName   string     `json:"file_name"`
	FileSize   int64      `json:"file_size"`
	ChunkCount uint32     `json:"chunk_count"`
	Seeds      []PeerInfo `json:"seeds"`
	Downloads  int64      `json:"downloads"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Chunk represents a data chunk with its content
type Chunk struct {
	Index uint32 `json:"index"`
	Hash  string `json:"hash"`
	Data  []byte `json:"data"`
}

// ComputeHash calculates the SHA-256 hash of the chunk data
func (c *Chunk) ComputeHash() string {
	hash := sha256.Sum256(c.Data)
	return hex.EncodeToString(hash[:])
}

// Verify checks if the chunk data matches its hash
func (c *Chunk) Verify() bool {
	return c.ComputeHash() == c.Hash
}

// GenerateFileID creates a unique file ID (currently using file hash directly)
func GenerateFileID(fileHash string) string {
	return fileHash
}
