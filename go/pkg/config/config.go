// Package config handles configuration loading and management
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

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
	BindAddress     string // Listen on this address (0.0.0.0 or 127.0.0.1)
	DiscoveryPort   int    // libp2p discovery port
	TransportPort   int    // MsQuic data transfer port
	QUICPort        int    // Kept for backward compat, maps to TransportPort
	PingIntervalSec int
	RetryBackoffSec int
	LocalhostMode   bool // Bind to 127.0.0.1 only

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
