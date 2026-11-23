// Package netx gives Magnetk one connection abstraction over two transports:
//
//   - QUIC via MsQuic (package transport): multiplexed streams, datagrams,
//     connection migration, no head-of-line blocking between streams.
//   - TCP + TLS 1.3 + yamux: works where UDP is blocked and in builds
//     without MsQuic (CGO_ENABLED=0 or -tags nomsquic).
//
// A Listener serves both on the same port number (UDP for QUIC, TCP for the
// fallback). On TCP the first byte is sniffed: a TLS handshake starts a
// Magnetk v2 session, anything else is handed to an optional legacy handler
// (Magnetk v1 framing used by older JS clients).
//
// Dial in "auto" mode races QUIC and TCP happy-eyeballs style: QUIC starts
// first, TCP joins after a short delay, the first to finish wins.
package netx

import (
	"context"
	"errors"
	"io"
	"time"
)

// Stream is a reliable, ordered, bidirectional byte stream.
type Stream interface {
	io.ReadWriteCloser
	// CloseWrite sends EOF to the peer after queued data; reads keep working.
	CloseWrite() error
	SetDeadline(t time.Time) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
}

// Conn is a multiplexed, encrypted connection. Either side may open streams.
type Conn interface {
	OpenStream(ctx context.Context) (Stream, error)
	AcceptStream(ctx context.Context) (Stream, error)

	// Unreliable datagrams (QUIC only; ErrNoDatagrams on TCP).
	SendDatagram(b []byte) error
	ReceiveDatagram(ctx context.Context) ([]byte, error)
	MaxDatagram() int

	RemoteAddr() string
	Protocol() string // "quic" or "tcp"
	RTT() time.Duration
	Done() <-chan struct{}
	Close() error
}

// Listener accepts Conns.
type Listener interface {
	Accept(ctx context.Context) (Conn, error)
	Port() int
	Close() error
}

var (
	// ErrNoDatagrams is returned by SendDatagram on transports without datagrams.
	ErrNoDatagrams = errors.New("netx: datagrams not supported on this connection")
	// ErrClosed is returned after Close.
	ErrClosed = errors.New("netx: closed")
)

// Mode selects which transports an Endpoint uses.
type Mode string

const (
	ModeAuto Mode = "auto" // QUIC preferred, TCP fallback
	ModeQUIC Mode = "quic" // QUIC only
	ModeTCP  Mode = "tcp"  // TCP+TLS only
)

// ParseMode accepts "auto", "quic", "tcp" (case-insensitive, empty = auto).
func ParseMode(s string) Mode {
	switch s {
	case "quic", "QUIC", "udp":
		return ModeQUIC
	case "tcp", "TCP":
		return ModeTCP
	default:
		return ModeAuto
	}
}
