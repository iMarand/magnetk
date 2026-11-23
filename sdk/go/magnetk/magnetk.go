// Package magnetk is the Go SDK for Magnetk: fast messaging, pipes and file
// transfer between nodes, connected through one or more relays (QUIC with a
// TCP/TLS fallback), peer-to-peer whenever the peers can reach each other.
//
// The SDK is a thin, stable facade over the engine in
// github.com/iMarand/magnetk/go (the same engine behind the Python, C and
// C++ SDKs and the magnetk command line tool):
//
//	n, err := magnetk.Connect(ctx, "relay.example.com:4002")
//	if err != nil { ... }
//	defer n.Close()
//
//	n.Subscribe(ctx, "chat.>", func(m *magnetk.Message) {
//	    fmt.Printf("%s: %s\n", m.From, m.Data)
//	})
//	n.Publish(ctx, "chat.general", []byte("hello"))
//
// Every type is an alias of the engine type, so values can be passed to the
// engine packages directly when lower-level control is needed.
package magnetk

import (
	"context"
	"errors"
	"time"

	"github.com/iMarand/magnetk/go/pkg/config"
	"github.com/iMarand/magnetk/go/pkg/files"
	link "github.com/iMarand/magnetk/go/pkg/magnetk"
	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/node"
	"github.com/iMarand/magnetk/go/pkg/relay"
	"github.com/iMarand/magnetk/go/pkg/transport"
	"github.com/iMarand/magnetk/go/pkg/wire"
)

// Version of the SDK and wire protocol.
const (
	Version         = "2.0.0"
	ProtocolVersion = 2
)

// ===== nodes =====

type (
	Node         = node.Node
	Config       = node.Config
	Info         = node.Info
	Stats        = node.Stats
	Event        = node.Event
	EventType    = node.EventType
	Route        = node.Route
	Message      = node.Message
	Kind         = node.Kind
	Subscription = node.Subscription
	SendOption   = node.SendOption
	NodeInfo     = wire.NodeInfo
	RemoteError  = node.RemoteError
)

// Message kinds.
const (
	KindDirect   = node.KindDirect
	KindPublish  = node.KindPublish
	KindRequest  = node.KindRequest
	KindPresence = node.KindPresence
)

// Data path policies for pipes and file transfers.
const (
	RouteAuto   = node.RouteAuto
	RouteDirect = node.RouteDirect
	RouteRelay  = node.RouteRelay
)

// Connection events.
const (
	EventConnected    = node.EventConnected
	EventDisconnected = node.EventDisconnected
	EventError        = node.EventError
)

// Errors (compare with errors.Is).
var (
	ErrClosed       = node.ErrClosed
	ErrNotConnected = node.ErrNotConnected
	ErrDisconnected = node.ErrDisconnected
	ErrUnauthorized = node.ErrUnauthorized
	ErrNoRoute      = node.ErrNoRoute
	ErrDropped      = node.ErrDropped
	ErrTimeout      = node.ErrTimeout
	ErrNoProviders  = node.ErrNoProviders
	ErrNotRequest   = node.ErrNotRequest
	ErrTooLarge     = node.ErrTooLarge
)

// Send options.
var (
	// WithReceipt waits until the relay delivered the message to the
	// receiver (or stored it, with WithStore).
	WithReceipt = node.WithReceipt
	// WithStore keeps the message for an offline receiver.
	WithStore = node.WithStore
	// Unreliable sends the message as a QUIC datagram: lowest latency, may
	// be lost or reordered, limited to about 1 KB.
	Unreliable = node.Unreliable
)

// New creates a node without connecting it; call Start. Most programs use
// Connect instead.
func New(cfg Config) (*Node, error) { return node.New(cfg) }

// Connect creates a node for the given relays (tried in order, with
// failover) and waits until it is connected or ctx ends.
func Connect(ctx context.Context, relays ...string) (*Node, error) {
	return ConnectConfig(ctx, Config{Relays: relays})
}

// ConnectConfig is Connect with full configuration.
func ConnectConfig(ctx context.Context, cfg Config) (*Node, error) {
	if len(cfg.Relays) == 0 {
		return nil, errors.New("magnetk: at least one relay is required")
	}
	n, err := node.New(cfg)
	if err != nil {
		return nil, err
	}
	if err := n.Start(ctx); err != nil {
		n.Close()
		return nil, err
	}
	if err := n.WaitConnected(ctx); err != nil {
		n.Close()
		return nil, err
	}
	return n, nil
}

// ===== pipes =====

type (
	Pipe              = node.Pipe
	PipeOption        = node.PipeOption
	PipeRejectedError = node.PipeRejectedError
)

var (
	// WithMeta attaches a string the receiver sees as Pipe.Meta.
	WithMeta = node.WithMeta
	// WithRoute overrides the node's route policy for one pipe.
	WithRoute = node.WithRoute
)

// ===== files =====

type (
	SharedFile      = node.SharedFile
	ShareOptions    = node.ShareOptions
	DownloadOptions = node.DownloadOptions
	Providers       = node.Providers
	Progress        = files.Progress
	DownloadResult  = files.Result
	Manifest        = files.Manifest
	Link            = link.MagnetLink
)

// ParseLink parses a "magnetk:?xt=urn:sha256:..." link.
func ParseLink(s string) (*Link, error) { return link.Parse(s) }

// HashFile builds the manifest (sha256 + per-chunk hashes) of a file.
// chunkSize 0 picks a size automatically.
func HashFile(ctx context.Context, path string, chunkSize int64) (*Manifest, error) {
	return files.BuildManifest(ctx, path, chunkSize, nil)
}

// VerifyFile checks that a file has the given sha256 hash.
func VerifyFile(path, hash string) error { return files.VerifyFile(path, hash) }

// ===== relays =====

type (
	Relay              = relay.Server
	RelayConfig        = relay.Config
	RelayStats         = relay.Stats
	SlowConsumerPolicy = relay.SlowConsumerPolicy
)

const (
	SlowDrop       = relay.SlowDrop
	SlowDisconnect = relay.SlowDisconnect
)

// StartRelay starts a relay (QUIC + TCP on cfg.Listen, default
// "0.0.0.0:4002"). Use Peers + ClusterSecret to federate several relays.
func StartRelay(cfg RelayConfig) (*Relay, error) {
	s, err := relay.New(cfg)
	if err != nil {
		return nil, err
	}
	if err := s.Start(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// ===== transport tuning =====

type (
	Mode          = netx.Mode
	QUICConfig    = transport.Config
	QUICProfile   = transport.Profile
	CongestionAlg = transport.Congestion
)

// Transport modes.
const (
	ModeAuto = netx.ModeAuto // QUIC, falling back to TCP+TLS
	ModeQUIC = netx.ModeQUIC
	ModeTCP  = netx.ModeTCP
)

// QUIC profiles and congestion controllers.
const (
	ProfileDefault       = transport.ProfileDefault
	ProfileLowLatency    = transport.ProfileLowLatency
	ProfileMaxThroughput = transport.ProfileMaxThroughput
	ProfileScavenger     = transport.ProfileScavenger
	ProfileRealTime      = transport.ProfileRealTime

	CongestionDefault = transport.CongestionDefault
	CongestionCubic   = transport.CongestionCubic
	CongestionBBR     = transport.CongestionBBR
)

// QUICAvailable reports whether the native QUIC transport (msquic) is
// available in this build; without it nodes and relays use TCP+TLS.
func QUICAvailable() bool { return transport.Available() }

// ===== configuration files =====

// Settings is a parsed settings.conf (KEY=VALUE, overridable with MAGNETK_*
// environment variables).
type Settings = config.File

// LoadSettings reads a settings file ("" = environment only).
func LoadSettings(path string) (*Settings, error) { return config.Load(path) }

// NodeConfigFromSettings builds a node configuration from a settings file.
func NodeConfigFromSettings(path string) (Config, error) {
	f, err := config.Load(path)
	if err != nil {
		return Config{}, err
	}
	f.ApplyRuntime()
	return f.Node(), nil
}

// RelayConfigFromSettings builds a relay configuration from a settings file.
func RelayConfigFromSettings(path string) (RelayConfig, error) {
	f, err := config.Load(path)
	if err != nil {
		return RelayConfig{}, err
	}
	f.ApplyRuntime()
	return f.Relay(), nil
}

// WithTimeout is a convenience for context.WithTimeout(context.Background(), d).
func WithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
