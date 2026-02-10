// Package chunk handles file chunking, hashing, and management
package chunk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"magnetk/pkg/types"
)

// Manager handles chunk operations for files
type Manager struct {
	chunkSize int64
}

// NewManager creates a new chunk Manager with the specified chunk size
func NewManager(chunkSizeBytes int64) *Manager {
	if chunkSizeBytes <= 0 {
		chunkSizeBytes = types.DefaultChunkSize
	}
	if chunkSizeBytes > types.MaxChunkSize {
		chunkSizeBytes = types.MaxChunkSize
	}
	return &Manager{
		chunkSize: chunkSizeBytes,
	}
}

// GenerateManifest creates a FileManifest for the given file
// It reads the file, splits it into chunks, and computes hashes
func (m *Manager) GenerateManifest(filePath string) (*types.FileManifest, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Get file info
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	fileName := info.Name()
	fileSize := info.Size()

	// Create manifest
	manifest := types.NewFileManifest(fileName, fileSize, uint32(m.chunkSize))

	// Compute file hash and chunk hashes
	fileHasher := sha256.New()
	buffer := make([]byte, m.chunkSize)
	chunkIndex := uint32(0)

	for {
		n, err := file.Read(buffer)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read file: %w", err)
		}

		// Compute chunk hash
		chunkData := buffer[:n]
		chunkHash := sha256.Sum256(chunkData)

		manifest.Chunks[chunkIndex] = types.ChunkInfo{
			Index: chunkIndex,
			Size:  uint32(n),
			Hash:  hex.EncodeToString(chunkHash[:]),
		}

		// Add to file hash
		fileHasher.Write(chunkData)
		chunkIndex++
	}

	manifest.FileHash = hex.EncodeToString(fileHasher.Sum(nil))
	manifest.FileID = types.GenerateFileID(manifest.FileHash)

	return manifest, nil
}

// ReadChunk reads a specific chunk from a file
func (m *Manager) ReadChunk(filePath string, chunkIndex uint32) (*types.Chunk, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Seek to chunk position
	offset := int64(chunkIndex) * m.chunkSize
	_, err = file.Seek(offset, io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("failed to seek to chunk: %w", err)
	}

	// Read chunk data
	buffer := make([]byte, m.chunkSize)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to read chunk: %w", err)
	}

	if n == 0 {
		return nil, fmt.Errorf("chunk index out of range")
	}

	chunkData := buffer[:n]
	hash := sha256.Sum256(chunkData)

	return &types.Chunk{
		Index: chunkIndex,
		Hash:  hex.EncodeToString(hash[:]),
		Data:  chunkData,
	}, nil
}

// ReadChunks reads multiple chunks from a file
func (m *Manager) ReadChunks(filePath string, indices []uint32) ([]*types.Chunk, error) {
	chunks := make([]*types.Chunk, 0, len(indices))

	for _, idx := range indices {
		chunk, err := m.ReadChunk(filePath, idx)
		if err != nil {
			return nil, fmt.Errorf("failed to read chunk %d: %w", idx, err)
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

// Writer handles writing chunks to a file
type Writer struct {
	filePath  string
	chunkSize int64
	file      *os.File
}

// NewWriter creates a new Writer for writing chunks to a file
func NewWriter(filePath string, fileSize int64, chunkSize int64) (*Writer, error) {
	// Create or truncate the file
	file, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create file: %w", err)
	}

	// Pre-allocate file size
	if err := file.Truncate(fileSize); err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to pre-allocate file: %w", err)
	}

	return &Writer{
		filePath:  filePath,
		chunkSize: chunkSize,
		file:      file,
	}, nil
}

// WriteChunk writes a chunk to the correct position in the file
func (w *Writer) WriteChunk(chunk *types.Chunk) error {
	// Verify chunk hash
	if !chunk.Verify() {
		return fmt.Errorf("chunk %d hash verification failed", chunk.Index)
	}

	// Seek to chunk position
	offset := int64(chunk.Index) * w.chunkSize
	_, err := w.file.Seek(offset, io.SeekStart)
	if err != nil {
		return fmt.Errorf("failed to seek to chunk position: %w", err)
	}

	// Write chunk data
	_, err = w.file.Write(chunk.Data)
	if err != nil {
		return fmt.Errorf("failed to write chunk: %w", err)
	}

	return nil
}

// Close closes the file writer
func (w *Writer) Close() error {
	return w.file.Close()
}

// VerifyFile verifies the complete file hash matches the expected hash
func VerifyFile(filePath string, expectedHash string) (bool, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return false, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	hasher := sha256.New()
	_, err = io.Copy(hasher, file)
	if err != nil {
		return false, fmt.Errorf("failed to hash file: %w", err)
	}

	actualHash := hex.EncodeToString(hasher.Sum(nil))
	return actualHash == expectedHash, nil
}
