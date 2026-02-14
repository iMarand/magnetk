// package magnetk provides utilities for creating and parsing magnet links
package magnetk

import (
	"fmt"
	"net/url"
	"strings"
)

// MagnetLink represents a parsed magnet link
type MagnetLink struct {
	FileHash   string // SHA-256 hash (xt=urn:sha256:...)
	FileName   string // Display name (dn=...)
	SeedID     string // Seed peer ID (xs=...)
	RelayAddr  string // Relay address (relay=...) - optional when DHT enabled
	FileSize   int64  // File size in bytes (xl=...)
	DHTEnabled bool   // DHT discovery enabled (dht=true)
}

// Parse parses a magnet link string into a MagnetLink struct
func Parse(magnetURI string) (*MagnetLink, error) {
	if !strings.HasPrefix(magnetURI, "magnetk:?") {
		return nil, fmt.Errorf("invalid magnet link: must start with 'magnetk:?'")
	}

	query := strings.TrimPrefix(magnetURI, "magnetk:?")
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

	// Parse relay (relay address) - now optional
	ml.RelayAddr = params.Get("relay")

	// Parse dht flag
	dhtParam := strings.ToLower(params.Get("dht"))
	ml.DHTEnabled = dhtParam == "true" || dhtParam == "1"

	// Parse xl (file size) if present
	if xl := params.Get("xl"); xl != "" {
		fmt.Sscanf(xl, "%d", &ml.FileSize)
	}

	// Validate: need at least one discovery method
	if ml.RelayAddr == "" && !ml.DHTEnabled {
		return nil, fmt.Errorf("magnet link needs either relay address or dht=true for discovery")
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

	// Relay is optional now
	if ml.RelayAddr != "" {
		params.Set("relay", ml.RelayAddr)
	}

	// DHT flag
	if ml.DHTEnabled {
		params.Set("dht", "true")
	}

	return "magnetk:?" + params.Encode()
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

// NewWithDHT creates a new MagnetLink with DHT enabled
func NewWithDHT(fileHash, fileName, seedID, relayAddr string, fileSize int64, dhtEnabled bool) *MagnetLink {
	return &MagnetLink{
		FileHash:   fileHash,
		FileName:   fileName,
		SeedID:     seedID,
		RelayAddr:  relayAddr,
		FileSize:   fileSize,
		DHTEnabled: dhtEnabled,
	}
}

// Validate checks if the magnet link has all required fields
func (ml *MagnetLink) Validate() error {
	if ml.FileHash == "" {
		return fmt.Errorf("file hash is required")
	}
	if ml.RelayAddr == "" && !ml.DHTEnabled {
		return fmt.Errorf("at least one discovery method required (relay address or dht=true)")
	}
	return nil
}

// HasRelay returns true if the magnet link has a relay address
func (ml *MagnetLink) HasRelay() bool {
	return ml.RelayAddr != ""
}

// HasDHT returns true if the magnet link has DHT enabled
func (ml *MagnetLink) HasDHT() bool {
	return ml.DHTEnabled
}
