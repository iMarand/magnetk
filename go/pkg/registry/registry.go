// Package registry handles file registration and lookup for the relay
package registry

import (
	"sync"
	"time"

	"magnetk/pkg/magnetk"
	"magnetk/pkg/types"
)

// FileRegistry maintains a registry of published files and their seeds
type FileRegistry struct {
	files     map[string]*types.FileEntry // key: file hash
	mu        sync.RWMutex
	relayAddr string
}

// NewFileRegistry creates a new FileRegistry
func NewFileRegistry(relayAddr string) *FileRegistry {
	return &FileRegistry{
		files:     make(map[string]*types.FileEntry),
		relayAddr: relayAddr,
	}
}

// PublishFile registers a file from a seed
func (r *FileRegistry) PublishFile(fileHash, fileName string, fileSize int64, chunkCount uint32, seedInfo types.PeerInfo) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, exists := r.files[fileHash]
	if !exists {
		// New file entry
		entry = &types.FileEntry{
			FileHash:   fileHash,
			FileName:   fileName,
			FileSize:   fileSize,
			ChunkCount: chunkCount,
			Seeds:      make([]types.PeerInfo, 0),
			Downloads:  0,
			CreatedAt:  time.Now(),
		}
		r.files[fileHash] = entry
	}

	// Add or update seed
	found := false
	for i, s := range entry.Seeds {
		if s.PeerID == seedInfo.PeerID {
			entry.Seeds[i] = seedInfo
			found = true
			break
		}
	}
	if !found {
		entry.Seeds = append(entry.Seeds, seedInfo)
	}

	// Generate magnet link
	ml := magnetk.New(fileHash, fileName, seedInfo.PeerID, r.relayAddr, fileSize)
	return ml.String()
}

// UnpublishFile removes a file from a seed
func (r *FileRegistry) UnpublishFile(fileHash, seedPeerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, exists := r.files[fileHash]
	if !exists {
		return
	}

	// Remove seed from list
	for i, s := range entry.Seeds {
		if s.PeerID == seedPeerID {
			entry.Seeds = append(entry.Seeds[:i], entry.Seeds[i+1:]...)
			break
		}
	}

	// Remove file entry if no seeds left
	if len(entry.Seeds) == 0 {
		delete(r.files, fileHash)
	}
}

// LookupFile finds seeds for a file by hash
func (r *FileRegistry) LookupFile(fileHash string) (*types.FileEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, exists := r.files[fileHash]
	if !exists {
		return nil, false
	}

	// Filter to online seeds only
	onlineSeeds := make([]types.PeerInfo, 0)
	for _, s := range entry.Seeds {
		if s.Online {
			onlineSeeds = append(onlineSeeds, s)
		}
	}

	// Return a copy with only online seeds
	result := &types.FileEntry{
		FileHash:   entry.FileHash,
		FileName:   entry.FileName,
		FileSize:   entry.FileSize,
		ChunkCount: entry.ChunkCount,
		Seeds:      onlineSeeds,
		Downloads:  entry.Downloads,
		CreatedAt:  entry.CreatedAt,
	}

	return result, true
}

// IncrementDownloads increments the download counter for a file
func (r *FileRegistry) IncrementDownloads(fileHash string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if entry, exists := r.files[fileHash]; exists {
		entry.Downloads++
	}
}

// UpdateSeedStatus updates the online status of a seed
func (r *FileRegistry) UpdateSeedStatus(peerID string, online bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, entry := range r.files {
		for i, s := range entry.Seeds {
			if s.PeerID == peerID {
				entry.Seeds[i].Online = online
				entry.Seeds[i].LastSeen = time.Now()
			}
		}
	}
}

// ListFiles returns all registered files
func (r *FileRegistry) ListFiles() []*types.FileEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entries := make([]*types.FileEntry, 0, len(r.files))
	for _, entry := range r.files {
		entries = append(entries, entry)
	}
	return entries
}

// Stats returns registry statistics
type RegistryStats struct {
	TotalFiles     int
	TotalSeeds     int
	TotalDownloads int64
}

func (r *FileRegistry) Stats() RegistryStats {
	r.mu.RLock()
	defer r.mu.RUnlock()

	stats := RegistryStats{
		TotalFiles: len(r.files),
	}

	seedSet := make(map[string]bool)
	for _, entry := range r.files {
		stats.TotalDownloads += entry.Downloads
		for _, s := range entry.Seeds {
			seedSet[s.PeerID] = true
		}
	}
	stats.TotalSeeds = len(seedSet)

	return stats
}
