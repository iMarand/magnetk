// Package magnet provides utilities for creating and parsing magnet links
package magnet

import (
	"fmt"
	"net/url"
	"strings"
)

// MagnetLink represents a parsed magnet link
type MagnetLink struct {
	FileHash  string // SHA-256 hash (xt=urn:sha256:...)
	FileName  string // Display name (dn=...)
	SeedID    string // Seed peer ID (xs=...)
	RelayAddr string // Required relay address (relay=...)
	FileSize  int64  // File size in bytes (xl=...)
}

// Parse parses a magnet link string into a MagnetLink struct
func Parse(magnetURI string) (*MagnetLink, error) {
	if !strings.HasPrefix(magnetURI, "magnet:?") {
		return nil, fmt.Errorf("invalid magnet link: must start with 'magnet:?'")
	}

	query := strings.TrimPrefix(magnetURI, "magnet:?")
	params, err := url.ParseQuery(query)
	if err != nil {
		return nil, fmt.Errorf("failed to parse magnet link: %w", err)
	}

	ml := &MagnetLink{}

	// Parse xt (exact topic - file hash)
	xt := params.Get("xt")
	if xt == "" {
		return nil, fmt.Errorf("magnet link missing xt (file hash)")
	}
	if strings.HasPrefix(xt, "urn:sha256:") {
		ml.FileHash = strings.TrimPrefix(xt, "urn:sha256:")
	} else {
		return nil, fmt.Errorf("unsupported hash type, expected urn:sha256:")
	}

	// Parse dn (display name)
	ml.FileName = params.Get("dn")

	// Parse xs (exact source - seed peer ID)
	ml.SeedID = params.Get("xs")

	// Parse relay (required relay address)
	ml.RelayAddr = params.Get("relay")
	if ml.RelayAddr == "" {
		return nil, fmt.Errorf("magnet link missing relay address")
	}

	// Parse xl (file size) if present
	if xl := params.Get("xl"); xl != "" {
		fmt.Sscanf(xl, "%d", &ml.FileSize)
	}

	return ml, nil
}

// String converts a MagnetLink to its string representation
func (ml *MagnetLink) String() string {
	params := url.Values{}

	// xt is required
	params.Set("xt", "urn:sha256:"+ml.FileHash)

	// Optional parameters
	if ml.FileName != "" {
		params.Set("dn", ml.FileName)
	}
	if ml.SeedID != "" {
		params.Set("xs", ml.SeedID)
	}
	if ml.FileSize > 0 {
		params.Set("xl", fmt.Sprintf("%d", ml.FileSize))
	}

	// relay is required for Magnet network
	params.Set("relay", ml.RelayAddr)

	return "magnet:?" + params.Encode()
}

// New creates a new MagnetLink
func New(fileHash, fileName, seedID, relayAddr string, fileSize int64) *MagnetLink {
	return &MagnetLink{
		FileHash:  fileHash,
		FileName:  fileName,
		SeedID:    seedID,
		RelayAddr: relayAddr,
		FileSize:  fileSize,
	}
}

// Validate checks if the magnet link has all required fields
func (ml *MagnetLink) Validate() error {
	if ml.FileHash == "" {
		return fmt.Errorf("file hash is required")
	}
	if ml.RelayAddr == "" {
		return fmt.Errorf("relay address is required")
	}
	return nil
}
