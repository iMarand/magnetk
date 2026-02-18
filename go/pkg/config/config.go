// Package config handles configuration loading and management
package config

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds all configuration values
// Config holds all configuration values
type Config struct {
	// Chunking settings
	ChunkSizeMB       int
	AdaptiveChunking  bool
	MaxParallelChunks int

	// Relay settings
	RelayAddress     string
	MaxRelayBufferGB int
	DiskBackedBuffer bool

	// Network settings
	BindAddress       string // Listen on this address (0.0.0.0 or 127.0.0.1)
	AdvertisedAddress string // Public IP to advertise to peers (e.g., VPS public IP)
	DiscoveryPort     int    // libp2p discovery port
	TransportPort     int    // MsQuic data transfer port
	QUICPort          int    // Kept for backward compat, maps to TransportPort
	PingIntervalSec   int
	RetryBackoffSec   int
	LocalhostMode     bool // Bind to 127.0.0.1 only

	// TLS / Security
	TLSCertFile      string // Path to TLS cert (empty = self-signed)
	TLSKeyFile       string // Path to TLS key
	NoCertValidation bool   // Skip cert validation (dev mode)

	// Performance
	MaxConnections          int
	MaxStreamsPerConnection int
	SendBufferSizeMB        int
	RecvBufferSizeMB        int

	// Environment
	Env string // development | staging | production

	// State persistence
	StateDir        string
	StateExpiryDays int

	// Logging
	LogLevel string
	LogFile  string

	// Relay-specific
	EnableTrafficRouting bool
	EnableAnalytics      bool

	// DHT settings
	DHTEnabled               bool     // Enable DHT discovery
	DHTMode                  string   // "server", "client", or "auto"
	DHTBootstrapPeers        []string // Custom bootstrap peer multiaddrs
	DHTReannounceIntervalMin int      // Re-announce interval in minutes

	// Multi-relay support
	RelayAddresses    []string // Array of relay multiaddrs for failover/scaling
	DiscoveryPriority []string // Discovery order: ["dht", "relay", "direct"]

	// Connection strategy
	RelayAssisted  string // "auto" | "true" | "false" — relay-assisted data transfer
	HolePunch      bool   // Enable NAT hole punching (STUN + libp2p)
	RelayTimeoutMs int    // How long to wait for direct before relay fallback
}

// DefaultConfig returns a Config with default values
func DefaultConfig() *Config {
	return &Config{
		ChunkSizeMB:             1,
		AdaptiveChunking:        true,
		MaxParallelChunks:       10,
		RelayAddress:            "0.0.0.0:4001",
		MaxRelayBufferGB:        1,
		DiskBackedBuffer:        false,
		BindAddress:             "0.0.0.0",
		DiscoveryPort:           4001,
		TransportPort:           4002,
		QUICPort:                4001,
		PingIntervalSec:         10,
		RetryBackoffSec:         5,
		LocalhostMode:           false,
		NoCertValidation:        true,
		MaxConnections:          1000,
		MaxStreamsPerConnection: 100,
		SendBufferSizeMB:        4,
		RecvBufferSizeMB:        4,
		Env:                     "development",
		StateDir:                "./transfer_states",
		StateExpiryDays:         7,
		LogLevel:                "info",
		LogFile:                 "./magnetk.log",
		EnableTrafficRouting:    true,
		EnableAnalytics:         true,

		// DHT defaults
		DHTEnabled:               false,
		DHTMode:                  "auto",
		DHTBootstrapPeers:        nil, // Uses default IPFS bootstrap nodes
		DHTReannounceIntervalMin: 30,

		// Multi-relay defaults
		RelayAddresses:    nil,
		DiscoveryPriority: []string{"dht", "relay", "direct"},

		// Connection strategy defaults
		RelayAssisted:  "auto",
		HolePunch:      true,
		RelayTimeoutMs: 5000,
	}
}

// Load reads configuration from a file
func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()

	cfg := DefaultConfig()
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse key=value
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue // Skip malformed lines
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if err := cfg.set(key, value); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Apply localhost mode
	if cfg.LocalhostMode {
		cfg.BindAddress = "127.0.0.1"
	}

	return cfg, nil
}

// LoadOrCreate loads a config file, or creates one with defaults if not found.
// This is useful for SDKs: when a user runs share.js or share.py for the first time,
// a settings.conf is automatically generated so they can customize it.
func LoadOrCreate(path string) (*Config, error) {
	// Try to load existing config
	cfg, err := Load(path)
	if err == nil {
		return cfg, nil
	}

	// If file doesn't exist, create it with defaults
	if !os.IsNotExist(unwrapErr(err)) {
		return nil, err // Real error, not just missing file
	}

	log.Printf("[config] Config not found at %s, creating default config...", path)

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create config directory: %w", err)
	}

	// Write default config
	if err := os.WriteFile(path, []byte(DefaultConfigContent()), 0644); err != nil {
		return nil, fmt.Errorf("failed to write default config: %w", err)
	}

	log.Printf("[config] Default config created at %s", path)
	return DefaultConfig(), nil
}

// unwrapErr extracts the innermost error for os.IsNotExist checks.
func unwrapErr(err error) error {
	for {
		inner := fmt.Errorf("%w", err)
		if inner == err {
			break
		}
		err = inner
	}
	// Try to find os-level error in the chain
	if os.IsNotExist(err) {
		return err
	}
	// Check if the error message indicates file not found
	if strings.Contains(err.Error(), "cannot find") || strings.Contains(err.Error(), "no such file") {
		return os.ErrNotExist
	}
	return err
}

// DefaultConfigContent returns the full default settings.conf content with documentation.
func DefaultConfigContent() string {
	return `# Magnetk P2P Configuration
# All values are optional - defaults are used when not specified
# This file was auto-generated. Edit as needed.

# ===== Chunking =====
CHUNK_SIZE_MB=1
ADAPTIVE_CHUNKING=true
MAX_PARALLEL_CHUNKS=10

# ===== Network Binding =====
# BIND_ADDRESS: 0.0.0.0 = all interfaces, 127.0.0.1 = localhost only
BIND_ADDRESS=0.0.0.0
# ADVERTISED_ADDRESS: Public IP to advertise (leave empty for STUN auto-detect)
ADVERTISED_ADDRESS=
DISCOVERY_PORT=4001
TRANSPORT_PORT=4002
LOCALHOST_MODE=false

# ===== Relay =====
# Single relay (backward compatible)
RELAY_ADDRESS=0.0.0.0:4001
MAX_RELAY_BUFFER_GB=1
DISK_BACKED_BUFFER=false

# Multi-relay: comma-separated multiaddrs for failover/scaling
# When one relay fails, the next is tried automatically.
# Example: /ip4/69.169.109.243/tcp/4001/p2p/12D3...,/ip4/1.2.3.4/tcp/4001/p2p/12D3...
RELAY_ADDRESSES=

# ===== DHT (Decentralized Discovery) =====
# Enable DHT for peer-to-peer discovery without central relay
DHT_ENABLED=false
# Mode: "server" (seeds/relays), "client" (downloaders), "auto" (auto-detect)
DHT_MODE=auto
# Custom bootstrap peers (comma-separated multiaddrs)
# Default: Public IPFS bootstrap nodes
DHT_BOOTSTRAP_PEERS=/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN,/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa,/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb,/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt,/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ
# How often seeds re-announce to DHT (minutes)
DHT_REANNOUNCE_INTERVAL_MIN=30

# ===== Discovery Priority =====
# Order to try discovery methods (comma-separated: dht, relay, direct)
DISCOVERY_PRIORITY=dht,relay,direct

# ===== TLS / Security =====
# Leave empty for auto-generated self-signed certificates
TLS_CERT_FILE=
TLS_KEY_FILE=
NO_CERT_VALIDATION=true

# ===== Performance =====
MAX_CONNECTIONS=1000
MAX_STREAMS_PER_CONNECTION=100
SEND_BUFFER_SIZE_MB=4
RECV_BUFFER_SIZE_MB=4

# ===== Environment =====
# development | staging | production
ENV=development

# ===== Connection Health =====
PING_INTERVAL_SEC=10
RETRY_BACKOFF_SEC=5

# ===== State Persistence =====
STATE_DIR=./transfer_states
STATE_EXPIRY_DAYS=7

# ===== Logging =====
LOG_LEVEL=info
LOG_FILE=./magnetk.log

# ===== Relay Features =====
ENABLE_TRAFFIC_ROUTING=true
ENABLE_ANALYTICS=true
`
}

// set assigns a value to a config field by key name
func (c *Config) set(key, value string) error {
	switch key {
	case "CHUNK_SIZE_MB":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid CHUNK_SIZE_MB: %w", err)
		}
		c.ChunkSizeMB = v
	case "ADAPTIVE_CHUNKING":
		c.AdaptiveChunking = strings.ToLower(value) == "true"
	case "MAX_PARALLEL_CHUNKS":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid MAX_PARALLEL_CHUNKS: %w", err)
		}
		c.MaxParallelChunks = v
	case "RELAY_ADDRESS":
		c.RelayAddress = value
	case "MAX_RELAY_BUFFER_GB":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid MAX_RELAY_BUFFER_GB: %w", err)
		}
		c.MaxRelayBufferGB = v
	case "DISK_BACKED_BUFFER":
		c.DiskBackedBuffer = strings.ToLower(value) == "true"
	case "BIND_ADDRESS":
		c.BindAddress = value
	case "ADVERTISED_ADDRESS":
		c.AdvertisedAddress = value
	case "DISCOVERY_PORT":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid DISCOVERY_PORT: %w", err)
		}
		c.DiscoveryPort = v
	case "TRANSPORT_PORT":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid TRANSPORT_PORT: %w", err)
		}
		c.TransportPort = v
	case "QUIC_PORT":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid QUIC_PORT: %w", err)
		}
		c.QUICPort = v
		c.TransportPort = v // Backward compat
	case "PING_INTERVAL_SEC":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid PING_INTERVAL_SEC: %w", err)
		}
		c.PingIntervalSec = v
	case "RETRY_BACKOFF_SEC":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid RETRY_BACKOFF_SEC: %w", err)
		}
		c.RetryBackoffSec = v
	case "LOCALHOST_MODE":
		c.LocalhostMode = strings.ToLower(value) == "true"
	case "TLS_CERT_FILE":
		c.TLSCertFile = value
	case "TLS_KEY_FILE":
		c.TLSKeyFile = value
	case "NO_CERT_VALIDATION":
		c.NoCertValidation = strings.ToLower(value) == "true"
	case "MAX_CONNECTIONS":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid MAX_CONNECTIONS: %w", err)
		}
		c.MaxConnections = v
	case "MAX_STREAMS_PER_CONNECTION":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid MAX_STREAMS_PER_CONNECTION: %w", err)
		}
		c.MaxStreamsPerConnection = v
	case "SEND_BUFFER_SIZE_MB":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid SEND_BUFFER_SIZE_MB: %w", err)
		}
		c.SendBufferSizeMB = v
	case "RECV_BUFFER_SIZE_MB":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid RECV_BUFFER_SIZE_MB: %w", err)
		}
		c.RecvBufferSizeMB = v
	case "ENV":
		c.Env = value
	case "STATE_DIR":
		c.StateDir = value
	case "STATE_EXPIRY_DAYS":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid STATE_EXPIRY_DAYS: %w", err)
		}
		c.StateExpiryDays = v
	case "LOG_LEVEL":
		c.LogLevel = value
	case "LOG_FILE":
		c.LogFile = value
	case "ENABLE_TRAFFIC_ROUTING":
		c.EnableTrafficRouting = strings.ToLower(value) == "true"
	case "ENABLE_ANALYTICS":
		c.EnableAnalytics = strings.ToLower(value) == "true"

	// DHT settings
	case "DHT_ENABLED":
		c.DHTEnabled = strings.ToLower(value) == "true"
	case "DHT_MODE":
		c.DHTMode = strings.ToLower(value)
	case "DHT_BOOTSTRAP_PEERS":
		if value != "" {
			peers := strings.Split(value, ",")
			for i := range peers {
				peers[i] = strings.TrimSpace(peers[i])
			}
			c.DHTBootstrapPeers = peers
		}
	case "DHT_REANNOUNCE_INTERVAL_MIN":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid DHT_REANNOUNCE_INTERVAL_MIN: %w", err)
		}
		c.DHTReannounceIntervalMin = v

	// Multi-relay
	case "RELAY_ADDRESSES":
		if value != "" {
			addrs := strings.Split(value, ",")
			for i := range addrs {
				addrs[i] = strings.TrimSpace(addrs[i])
			}
			c.RelayAddresses = addrs
		}
	case "DISCOVERY_PRIORITY":
		if value != "" {
			priority := strings.Split(value, ",")
			for i := range priority {
				priority[i] = strings.TrimSpace(strings.ToLower(priority[i]))
			}
			c.DiscoveryPriority = priority
		}

	// Connection strategy
	case "RELAY_ASSISTED":
		c.RelayAssisted = strings.ToLower(value)
	case "HOLE_PUNCH":
		c.HolePunch = strings.ToLower(value) == "true"
	case "RELAY_TIMEOUT_MS":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid RELAY_TIMEOUT_MS: %w", err)
		}
		c.RelayTimeoutMs = v
	}
	return nil
}

// ChunkSizeBytes returns the chunk size in bytes
func (c *Config) ChunkSizeBytes() int64 {
	return int64(c.ChunkSizeMB) * 1024 * 1024
}

// MaxRelayBufferBytes returns the max relay buffer size in bytes
func (c *Config) MaxRelayBufferBytes() int64 {
	return int64(c.MaxRelayBufferGB) * 1024 * 1024 * 1024
}

// SendBufferSizeBytes returns the send buffer size in bytes
func (c *Config) SendBufferSizeBytes() uint32 {
	return uint32(c.SendBufferSizeMB) * 1024 * 1024
}

// RecvBufferSizeBytes returns the recv buffer size in bytes
func (c *Config) RecvBufferSizeBytes() uint32 {
	return uint32(c.RecvBufferSizeMB) * 1024 * 1024
}

// EffectiveBindAddress returns the bind address, respecting localhost mode
func (c *Config) EffectiveBindAddress() string {
	if c.LocalhostMode {
		return "127.0.0.1"
	}
	return c.BindAddress
}

// IsDevelopment returns true if running in development mode
func (c *Config) IsDevelopment() bool {
	return c.Env == "development" || c.Env == ""
}
