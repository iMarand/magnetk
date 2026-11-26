// Package node is the Magnetk client engine used by every SDK.
//
// A Node keeps one connection to a relay (failing over between the relays
// it is given and reconnecting automatically) and offers:
//
//   - messaging: direct messages, publish/subscribe with wildcards and queue
//     groups, request/reply, delivery receipts, offline storage, and
//     unreliable low-latency datagrams;
//   - pipes: reliable byte streams to another node, peer-to-peer when the
//     peers can reach each other and spliced through the relay otherwise;
//   - files: content-addressed sharing and parallel, multi-source,
//     resumable, verified downloads.
package node

import (
	"errors"
	"github.com/iMarand/magnetk/go/pkg/identity"
	"log"
	"time"

	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/transport"
)

// Route selects the data path for pipes and file transfers.
type Route string

const (
	RouteAuto   Route = "auto"   // try peer-to-peer first, fall back to the relay
	RouteDirect Route = "direct" // peer-to-peer only
	RouteRelay  Route = "relay"  // always through the relay (works behind any NAT)
)

// Config configures a Node. Only Relays is required.
type Config struct {
	// Identity
	ID    string // node id; empty = assigned by the relay
	Token string // auth token, if the relay requires one
	Meta  string // free-form metadata shown to other nodes (e.g. JSON)
	// Key signs the HELLO so relays bind ID to it (nobody else can use the
	// ID afterwards); nil = no identity. See identity.LoadOrCreate.
	Key *identity.Key

	// Relays to use, in preference order ("host:port"). The node connects to
	// the first that answers and fails over to the others.
	Relays []string

	// Transport: auto (QUIC, TCP fallback), quic or tcp.
	Transport netx.Mode
	QUIC      transport.Config

	// TLS. VerifyPeer requires relays/peers to present certificates trusted
	// by the system or by CAFile; leave false for self-signed deployments.
	VerifyPeer bool
	CAFile     string
	CertFile   string // used when Listen is enabled
	KeyFile    string

	// Direct (peer-to-peer) data path.
	Listen         bool     // accept direct connections from other nodes
	ListenAddr     string   // default "0.0.0.0:0"
	AdvertiseAddrs []string // extra "host:port" addresses to tell peers (e.g. a VPS public IP)
	UPnP           bool     // ask the router to forward the listen port
	Route          Route    // data path policy (default auto)
	DirectTimeout  time.Duration

	// Messaging
	WatchPresence  bool
	InboxSize      int   // buffered incoming messages (default 8192)
	SubBuffer      int   // per-subscription handler buffer (default 1024)
	SendQueueBytes int64 // outbound queue bound (default 16 MiB)
	// SlowConsumerWait is how long delivery waits for a full subscription
	// handler or inbox before dropping (default 250ms, <0 = drop at once).
	SlowConsumerWait time.Duration
	RequestTimeout   time.Duration // default for requests and receipts (default 10s)

	// Reconnect backoff
	ReconnectMin time.Duration // default 250ms
	ReconnectMax time.Duration // default 10s

	// Liveness: a ping is sent every HeartbeatInterval; if nothing is heard
	// from the relay for HeartbeatTimeout the connection is considered dead
	// and the node fails over. Defaults 5s / 15s.
	HeartbeatInterval time.Duration
	HeartbeatTimeout  time.Duration

	// Files
	ChunkSize        int64    // 0 = automatic
	StreamsPerSource int      // parallel streams per provider (default 4)
	CacheDir         string   // manifest cache ("" = user cache dir, "-" = off)
	LinkRelays       []string // relays written into share links (default Relays)

	Logger *log.Logger
	// OnEvent is called for connection state changes (from a background
	// goroutine; must not block).
	OnEvent func(Event)
}

func (c *Config) defaults() {
	if c.Transport == "" {
		c.Transport = netx.ModeAuto
	}
	if c.ListenAddr == "" {
		c.ListenAddr = "0.0.0.0:0"
	}
	if c.Route == "" {
		c.Route = RouteAuto
	}
	if c.DirectTimeout == 0 {
		if c.Route == RouteDirect {
			c.DirectTimeout = 8 * time.Second
		} else {
			c.DirectTimeout = 2 * time.Second
		}
	}
	if c.InboxSize == 0 {
		c.InboxSize = 8192
	}
	if c.SubBuffer == 0 {
		c.SubBuffer = 1024
	}
	if c.SlowConsumerWait == 0 {
		c.SlowConsumerWait = 250 * time.Millisecond
	} else if c.SlowConsumerWait < 0 {
		c.SlowConsumerWait = time.Nanosecond
	}
	if c.SendQueueBytes == 0 {
		c.SendQueueBytes = 16 << 20
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = 10 * time.Second
	}
	if c.ReconnectMin == 0 {
		c.ReconnectMin = 250 * time.Millisecond
	}
	if c.ReconnectMax == 0 {
		c.ReconnectMax = 10 * time.Second
	}
	if c.HeartbeatInterval == 0 {
		c.HeartbeatInterval = 5 * time.Second
	}
	if c.HeartbeatTimeout == 0 {
		c.HeartbeatTimeout = 3 * c.HeartbeatInterval
	}
	if c.StreamsPerSource == 0 {
		c.StreamsPerSource = 4
	}
}

// EventType identifies node events.
type EventType string

const (
	EventConnected    EventType = "connected"
	EventDisconnected EventType = "disconnected"
	EventError        EventType = "error"
)

// Event reports a connection state change.
type Event struct {
	Type     EventType
	Relay    string // relay address
	RelayID  string
	Protocol string // "quic" or "tcp"
	Err      error
}

var (
	ErrClosed       = errors.New("magnetk: node closed")
	ErrNotConnected = errors.New("magnetk: not connected to a relay")
	ErrDisconnected = errors.New("magnetk: relay connection lost")
	ErrUnauthorized = errors.New("magnetk: relay rejected the token or identity")
	ErrNoRoute      = errors.New("magnetk: destination is not connected")
	ErrDropped      = errors.New("magnetk: message dropped (receiver too slow)")
	ErrTimeout      = errors.New("magnetk: timed out")
	ErrNoProviders  = errors.New("magnetk: no providers found for this file")
	ErrNotRequest   = errors.New("magnetk: message is not a request")
	ErrTooLarge     = errors.New("magnetk: message too large")
)

// RemoteError is an error returned by the remote request handler.
type RemoteError struct{ Msg string }

func (e *RemoteError) Error() string { return "magnetk: remote error: " + e.Msg }
