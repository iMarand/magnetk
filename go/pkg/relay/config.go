// Package relay implements the Magnetk relay: a QUIC (+TCP fallback) server
// that nodes connect to for
//
//   - direct messages, publish/subscribe (wildcards, queue groups) and
//     request/reply, with optional delivery receipts and offline storage;
//   - presence and node lookup;
//   - a file registry (who provides which content hash);
//   - pipes: byte streams between two nodes spliced through the relay when
//     they cannot reach each other directly;
//   - federation: several relays (e.g. two VPSes) form a mesh, so nodes on
//     different relays can message, subscribe and open pipes to each other.
//
// One relay process can serve thousands of nodes; every connection costs a
// few goroutines and a bounded outbound queue.
package relay

import (
	"log"
	"time"

	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/transport"
)

// SlowConsumerPolicy decides what happens when a node cannot keep up.
type SlowConsumerPolicy string

const (
	// SlowDrop drops messages for that node (counted in stats).
	SlowDrop SlowConsumerPolicy = "drop"
	// SlowDisconnect disconnects the node so it can reconnect fresh.
	SlowDisconnect SlowConsumerPolicy = "disconnect"
)

// Config configures a relay Server.
type Config struct {
	// ID identifies the relay in a federation (default: random).
	ID string
	// Listen is the QUIC+TCP address, e.g. "0.0.0.0:4002".
	Listen string
	// LegacyListen serves Magnetk v1 clients (JS SDK) on plain TCP,
	// e.g. "0.0.0.0:4003". Empty disables it. Plain (non-TLS) TCP on Listen
	// is always routed to the legacy handler as well.
	LegacyListen string

	Mode     netx.Mode
	CertFile string
	KeyFile  string
	CertHash string // Windows store certificate thumbprint
	QUIC     transport.Config

	// AuthTokens, when non-empty, are the tokens nodes must present.
	AuthTokens []string

	// IdentitiesFile persists which key owns which node id ("" = memory
	// only). A node id claimed with a key cannot be used without that key.
	IdentitiesFile string
	// RequireIdentity rejects nodes that do not sign their HELLO.
	RequireIdentity bool

	// Peers are other relays ("host:port") to federate with.
	Peers []string
	// ClusterSecret must match on every relay of a federation.
	ClusterSecret string

	MaxFrame          int                // largest message frame (default 16 MiB)
	MaxQueueBytes     int64              // per-node outbound queue (default 32 MiB)
	SlowConsumer      SlowConsumerPolicy // default drop
	MaxSessions       int                // default 100000
	MaxSubsPerSession int                // default 10000
	MaxPipes          int                // concurrent relayed pipes (default 10000)
	PipeBufferBytes   int                // copy buffer per pipe direction (default 256 KiB)

	OfflineMaxMsgs  int           // stored messages per offline node (default 1000)
	OfflineMaxBytes int64         // total offline storage (default 256 MiB)
	OfflineTTL      time.Duration // default 1h

	// SessionTimeout closes a node or link that sent nothing for this long
	// (nodes ping every 5s by default). Default 45s.
	SessionTimeout time.Duration

	RateLimit float64 // messages/s per node, 0 = unlimited
	RateBurst int     // default 2 x RateLimit

	Logger *log.Logger
}

func (c *Config) defaults() {
	if c.Listen == "" {
		c.Listen = "0.0.0.0:4002"
	}
	if c.Mode == "" {
		c.Mode = netx.ModeAuto
	}
	if c.MaxFrame == 0 {
		c.MaxFrame = 16 << 20
	}
	if c.MaxQueueBytes == 0 {
		c.MaxQueueBytes = 32 << 20
	}
	if c.SlowConsumer == "" {
		c.SlowConsumer = SlowDrop
	}
	if c.MaxSessions == 0 {
		c.MaxSessions = 100000
	}
	if c.MaxSubsPerSession == 0 {
		c.MaxSubsPerSession = 10000
	}
	if c.MaxPipes == 0 {
		c.MaxPipes = 10000
	}
	if c.PipeBufferBytes == 0 {
		c.PipeBufferBytes = 256 << 10
	}
	if c.OfflineMaxMsgs == 0 {
		c.OfflineMaxMsgs = 1000
	}
	if c.OfflineMaxBytes == 0 {
		c.OfflineMaxBytes = 256 << 20
	}
	if c.OfflineTTL == 0 {
		c.OfflineTTL = time.Hour
	}
	if c.SessionTimeout == 0 {
		c.SessionTimeout = 45 * time.Second
	}
	if c.RateLimit > 0 && c.RateBurst == 0 {
		c.RateBurst = int(2 * c.RateLimit)
		if c.RateBurst < 1 {
			c.RateBurst = 1
		}
	}
}
