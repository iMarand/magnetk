// Package state handles transfer state persistence and resume capability
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"magnet/pkg/types"
)

// Manager handles state persistence for transfer resume
type Manager struct {
	stateDir   string
	expiryDays int
}

// NewManager creates a new state Manager
func NewManager(stateDir string, expiryDays int) (*Manager, error) {
	// Create state directory if it doesn't exist
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}

	return &Manager{
		stateDir:   stateDir,
		expiryDays: expiryDays,
	}, nil
}

// stateFilePath returns the path to the state file for a transfer
func (m *Manager) stateFilePath(transferID string) string {
	return filepath.Join(m.stateDir, transferID+".json")
}

// Save persists a transfer state to disk
func (m *Manager) Save(state *types.TransferState) error {
	state.LastUpdated = time.Now()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	filePath := m.stateFilePath(state.TransferID)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	return nil
}

// Load retrieves a transfer state from disk
func (m *Manager) Load(transferID string) (*types.TransferState, error) {
	filePath := m.stateFilePath(transferID)

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No state file exists
		}
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	var state types.TransferState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal state: %w", err)
	}

	// Check if state is expired
	if m.isExpired(&state) {
		// Delete expired state file
		os.Remove(filePath)
		return nil, nil
	}

	return &state, nil
}

// Delete removes a transfer state from disk
func (m *Manager) Delete(transferID string) error {
	filePath := m.stateFilePath(transferID)
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete state file: %w", err)
	}
	return nil
}

// isExpired checks if a state has expired
func (m *Manager) isExpired(state *types.TransferState) bool {
	expiryTime := state.LastUpdated.Add(time.Duration(m.expiryDays) * 24 * time.Hour)
	return time.Now().After(expiryTime)
}

// ListStates returns all transfer state IDs
func (m *Manager) ListStates() ([]string, error) {
	entries, err := os.ReadDir(m.stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read state directory: %w", err)
	}

	var ids []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			id := entry.Name()[:len(entry.Name())-5] // Remove .json
			ids = append(ids, id)
		}
	}

	return ids, nil
}

// CleanExpired removes all expired state files
func (m *Manager) CleanExpired() (int, error) {
	ids, err := m.ListStates()
	if err != nil {
		return 0, err
	}

	cleaned := 0
	for _, id := range ids {
		state, err := m.Load(id)
		if err != nil {
			continue
		}
		if state == nil {
			// Already deleted due to expiry
			cleaned++
		}
	}

	return cleaned, nil
}

// FindByFileHash finds a transfer state by file hash
func (m *Manager) FindByFileHash(fileHash string) (*types.TransferState, error) {
	ids, err := m.ListStates()
	if err != nil {
		return nil, err
	}

	for _, id := range ids {
		state, err := m.Load(id)
		if err != nil || state == nil {
			continue
		}
		if (state.Manifest != nil && state.Manifest.FileHash == fileHash) || state.FileHash == fileHash {
			return state, nil
		}
	}

	return nil, nil
}
