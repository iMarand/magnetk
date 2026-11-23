// Package transport is the Go binding of the MsQuic-based C transport layer
// (c/src/transport.c).
//
// The C layer is driven in non-blocking mode: every call into C returns
// immediately and readiness is reported back through a notification callback,
// so a blocked Read/Write/Accept parks a goroutine, never an OS thread. That
// keeps CPU and memory flat even with thousands of connections.
//
// Build tags:
//
//	default (cgo enabled)     MsQuic transport is available.
//	-tags nomsquic or CGO=0   stub that reports ErrUnavailable; callers fall
//	                          back to the TCP transport in package netx.
package transport

import (
	"errors"
	"time"
)

// Profile selects the MsQuic execution profile. The zero value is
// ProfileMaxThroughput.
type Profile int

const (
	ProfileDefault       Profile = 0 // = ProfileMaxThroughput
	ProfileLowLatency    Profile = 1
	ProfileMaxThroughput Profile = 2
	ProfileScavenger     Profile = 3
	ProfileRealTime      Profile = 4
)

// Congestion selects the congestion control algorithm. The zero value is
// Cubic. BBR (a preview feature of MsQuic 2.x) is available but opt-in: in
// testing its pacing could collapse after a burst on an otherwise idle
// connection, leaving later transfers at a few MB/s with no packet loss.
type Congestion int

const (
	CongestionDefault Congestion = 0 // = CongestionCubic
	CongestionCubic   Congestion = 1
	CongestionBBR     Congestion = 2
)

// ParseProfile maps "low_latency", "max_throughput", "scavenger", "realtime".
func ParseProfile(s string) Profile {
	switch s {
	case "low_latency", "lowlat", "latency":
		return ProfileLowLatency
	case "max_throughput", "maxtput", "throughput":
		return ProfileMaxThroughput
	case "scavenger":
		return ProfileScavenger
	case "realtime", "real_time":
		return ProfileRealTime
	}
	return ProfileDefault
}

// ParseCongestion maps "cubic" / "bbr".
func ParseCongestion(s string) Congestion {
	switch s {
	case "cubic":
		return CongestionCubic
	case "bbr":
		return CongestionBBR
	}
	return CongestionDefault
}

func (p Profile) cValue() int {
	switch p {
	case ProfileLowLatency:
		return 0
	case ProfileScavenger:
		return 2
	case ProfileRealTime:
		return 3
	default:
		return 1
	}
}

func (c Congestion) cValue() int {
	if c == CongestionBBR {
		return 1
	}
	return 0
}

// Config holds transport options. Zero values fall back to DefaultConfig.
type Config struct {
	BindAddress string // listen address ("0.0.0.0", "127.0.0.1", "::")
	Port        uint16 // listen port, 0 = ephemeral

	// TLS. Linux/macOS: PEM CertFile + KeyFile are required to listen.
	// Windows: CertHash (SHA-1 thumbprint in the "My" store) or a generated
	// self-signed certificate when empty.
	CertFile         string
	KeyFile          string
	CertHash         string
	CertMachineStore bool
	NoCertValidation bool // client side: accept any server certificate

	ALPN string

	MaxConnections    uint32
	MaxStreamsPerConn uint16

	IdleTimeout      time.Duration
	HandshakeTimeout time.Duration
	KeepAlive        time.Duration // 0 = 15s default, negative = off

	SendWindow uint32 // per-stream unacknowledged bytes
	RecvWindow uint32 // per-stream receive window (rounded up to a power of 2)
	ConnWindow uint32 // per-connection receive window, 0 = 4 x RecvWindow

	Profile          Profile
	Congestion       Congestion
	DisableDatagrams bool
}

// DefaultConfig returns settings tuned for bulk transfer and messaging.
// A zero Config means the same thing; DefaultConfig just spells it out.
func DefaultConfig() Config {
	return Config{
		BindAddress:       "0.0.0.0",
		Port:              4002,
		NoCertValidation:  true,
		ALPN:              "magnetk/2",
		MaxConnections:    10000,
		MaxStreamsPerConn: 1024,
		IdleTimeout:       60 * time.Second,
		HandshakeTimeout:  10 * time.Second,
		KeepAlive:         15 * time.Second,
		SendWindow:        8 << 20,
		RecvWindow:        8 << 20,
		Profile:           ProfileMaxThroughput,
		Congestion:        CongestionCubic,
	}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.ALPN == "" {
		c.ALPN = d.ALPN
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = d.MaxConnections
	}
	if c.MaxStreamsPerConn == 0 {
		c.MaxStreamsPerConn = d.MaxStreamsPerConn
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = d.IdleTimeout
	}
	if c.HandshakeTimeout == 0 {
		c.HandshakeTimeout = d.HandshakeTimeout
	}
	if c.KeepAlive == 0 {
		c.KeepAlive = d.KeepAlive
	} else if c.KeepAlive < 0 {
		c.KeepAlive = 0
	}
	if c.SendWindow == 0 {
		c.SendWindow = d.SendWindow
	}
	if c.RecvWindow == 0 {
		c.RecvWindow = d.RecvWindow
	}
	return c
}

// ConnStats are MsQuic connection statistics.
type ConnStats struct {
	RTT              time.Duration
	MinRTT           time.Duration
	CongestionWindow uint32
	PathMTU          uint32
	SentBytes        uint64
	RecvBytes        uint64
	SentPackets      uint64
	LostPackets      uint64
}

var (
	// ErrUnavailable is returned when the binary was built without MsQuic.
	ErrUnavailable = errors.New("transport: MsQuic not available in this build")
	// ErrClosed is returned for operations on a closed object.
	ErrClosed = errors.New("transport: closed")
	// ErrReset means the peer aborted the stream.
	ErrReset = errors.New("transport: stream reset by peer")
	// ErrTimeout means a connect or handshake did not finish in time.
	ErrTimeout = errors.New("transport: timeout")
	// ErrConnect means the remote end refused or could not be reached.
	ErrConnect = errors.New("transport: connection failed")
	// ErrTLS means the TLS handshake failed.
	ErrTLS = errors.New("transport: TLS handshake failed")
	// ErrDatagramTooLarge means the datagram exceeds the path limit.
	ErrDatagramTooLarge = errors.New("transport: datagram too large")
	// ErrDatagramUnavailable means datagrams were not negotiated.
	ErrDatagramUnavailable = errors.New("transport: datagrams not available")
)
