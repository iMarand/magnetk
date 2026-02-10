// Package buffer implements a chunk buffer for the relay node
package buffer

import (
	"container/list"
	"fmt"
	"sync"
	"time"

	"magnetk/pkg/types"
)

// BufferedChunk represents a chunk stored in the buffer
type BufferedChunk struct {
	Chunk      *types.Chunk
	TargetPeer string
	FileID     string
	ReceivedAt time.Time
}

// ChunkBuffer manages buffered chunks for offline peers
type ChunkBuffer struct {
	maxSize     int64                    // Maximum buffer size in bytes
	currentSize int64                    // Current buffer size in bytes
	chunks      *list.List               // LRU list of buffered chunks
	chunkMap    map[string]*list.Element // Map for quick lookup [targetPeer:fileID:chunkIndex]
	mu          sync.Mutex
}

// NewChunkBuffer creates a new ChunkBuffer with the specified max size
func NewChunkBuffer(maxSizeBytes int64) *ChunkBuffer {
	return &ChunkBuffer{
		maxSize:     maxSizeBytes,
		currentSize: 0,
		chunks:      list.New(),
		chunkMap:    make(map[string]*list.Element),
	}
}

// key generates a unique key for a buffered chunk
func key(targetPeer, fileID string, chunkIndex uint32) string {
	return fmt.Sprintf("%s:%s:%d", targetPeer, fileID, chunkIndex)
}

// Store adds a chunk to the buffer for an offline peer
// Returns false if the buffer is full and cannot evict enough space
func (b *ChunkBuffer) Store(chunk *types.Chunk, targetPeer, fileID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	chunkSize := int64(len(chunk.Data))
	k := key(targetPeer, fileID, chunk.Index)

	// Check if already buffered
	if _, exists := b.chunkMap[k]; exists {
		return true
	}

	// Evict chunks if necessary
	for b.currentSize+chunkSize > b.maxSize && b.chunks.Len() > 0 {
		b.evictOldest()
	}

	// Check if there's still not enough space
	if b.currentSize+chunkSize > b.maxSize {
		return false
	}

	// Add to buffer
	bc := &BufferedChunk{
		Chunk:      chunk,
		TargetPeer: targetPeer,
		FileID:     fileID,
		ReceivedAt: time.Now(),
	}

	elem := b.chunks.PushBack(bc)
	b.chunkMap[k] = elem
	b.currentSize += chunkSize

	return true
}

// evictOldest removes the oldest chunk from the buffer
func (b *ChunkBuffer) evictOldest() {
	oldest := b.chunks.Front()
	if oldest == nil {
		return
	}

	bc := oldest.Value.(*BufferedChunk)
	k := key(bc.TargetPeer, bc.FileID, bc.Chunk.Index)

	delete(b.chunkMap, k)
	b.chunks.Remove(oldest)
	b.currentSize -= int64(len(bc.Chunk.Data))
}

// GetChunksForPeer retrieves all buffered chunks for a peer
func (b *ChunkBuffer) GetChunksForPeer(peerID string) []*BufferedChunk {
	b.mu.Lock()
	defer b.mu.Unlock()

	result := make([]*BufferedChunk, 0)

	for elem := b.chunks.Front(); elem != nil; elem = elem.Next() {
		bc := elem.Value.(*BufferedChunk)
		if bc.TargetPeer == peerID {
			result = append(result, bc)
		}
	}

	return result
}

// RemoveChunksForPeer removes all buffered chunks for a peer
func (b *ChunkBuffer) RemoveChunksForPeer(peerID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	toRemove := make([]*list.Element, 0)

	for elem := b.chunks.Front(); elem != nil; elem = elem.Next() {
		bc := elem.Value.(*BufferedChunk)
		if bc.TargetPeer == peerID {
			toRemove = append(toRemove, elem)
		}
	}

	for _, elem := range toRemove {
		bc := elem.Value.(*BufferedChunk)
		k := key(bc.TargetPeer, bc.FileID, bc.Chunk.Index)
		delete(b.chunkMap, k)
		b.chunks.Remove(elem)
		b.currentSize -= int64(len(bc.Chunk.Data))
	}

	return len(toRemove)
}

// RemoveChunk removes a specific chunk from the buffer
func (b *ChunkBuffer) RemoveChunk(targetPeer, fileID string, chunkIndex uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()

	k := key(targetPeer, fileID, chunkIndex)
	elem, exists := b.chunkMap[k]
	if !exists {
		return
	}

	bc := elem.Value.(*BufferedChunk)
	delete(b.chunkMap, k)
	b.chunks.Remove(elem)
	b.currentSize -= int64(len(bc.Chunk.Data))
}

// Stats returns buffer statistics
type BufferStats struct {
	MaxSizeBytes     int64
	CurrentSizeBytes int64
	ChunkCount       int
	UsagePercent     float64
}

func (b *ChunkBuffer) Stats() BufferStats {
	b.mu.Lock()
	defer b.mu.Unlock()

	usage := float64(0)
	if b.maxSize > 0 {
		usage = float64(b.currentSize) / float64(b.maxSize) * 100
	}

	return BufferStats{
		MaxSizeBytes:     b.maxSize,
		CurrentSizeBytes: b.currentSize,
		ChunkCount:       b.chunks.Len(),
		UsagePercent:     usage,
	}
}

// IsFull returns true if the buffer is full
func (b *ChunkBuffer) IsFull() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentSize >= b.maxSize
}

// Available returns the available space in bytes
func (b *ChunkBuffer) Available() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.maxSize - b.currentSize
}
