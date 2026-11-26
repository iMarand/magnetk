// Package config loads Magnetk settings from a KEY=VALUE file
// (config/settings.conf) and MAGNETK_<KEY> environment variables, and maps
// them onto relay and node configurations.
//
// Precedence: command-line flags > environment > file > built-in defaults.
// A missing file is not an error: everything has a sensible default.
package config

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/node"
	"github.com/iMarand/magnetk/go/pkg/relay"
	"github.com/iMarand/magnetk/go/pkg/transport"
)

// DefaultPath is where tools look for the settings file.
const DefaultPath = "./config/settings.conf"

// File holds raw settings.
type File struct {
	Path   string
	values map[string]string
}

// Load reads path (if it exists) and overlays MAGNETK_* environment variables.
func Load(path string) (*File, error) {
	f := &File{Path: path, values: map[string]string{}}
	if path != "" {
		fh, err := os.Open(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			defer fh.Close()
			sc := bufio.NewScanner(fh)
			line := 0
			for sc.Scan() {
				line++
				t := strings.TrimSpace(sc.Text())
				if t == "" || strings.HasPrefix(t, "#") {
					continue
				}
				k, v, ok := strings.Cut(t, "=")
				if !ok {
					return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, line)
				}
				f.values[strings.ToUpper(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"'`)
			}
			if err := sc.Err(); err != nil {
				return nil, err
			}
		}
	}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(k, "MAGNETK_") && k != "MAGNETK_TRACE" {
			f.values[strings.TrimPrefix(k, "MAGNETK_")] = v
		}
	}
	return f, nil
}

// Set overrides a value (used for command-line flags).
func (f *File) Set(key, value string) { f.values[strings.ToUpper(key)] = value }

// Has reports whether key was set anywhere.
func (f *File) Has(key string) bool { _, ok := f.values[key]; return ok }

// Str returns a string setting.
func (f *File) Str(key, def string) string {
	if v, ok := f.values[key]; ok && v != "" {
		return v
	}
	return def
}

// Int returns an integer setting.
func (f *File) Int(key string, def int) int {
	if v, ok := f.values[key]; ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		log.Printf("[config] %s=%q is not a number; using %d", key, v, def)
	}
	return def
}

// Float returns a float setting.
func (f *File) Float(key string, def float64) float64 {
	if v, ok := f.values[key]; ok && v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			return n
		}
	}
	return def
}

// Bool returns a boolean setting (true/false/1/0/yes/no/on/off).
func (f *File) Bool(key string, def bool) bool {
	v, ok := f.values[key]
	if !ok || v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

// Dur returns a duration ("500ms", "10s", "2m"; a bare number is seconds).
func (f *File) Dur(key string, def time.Duration) time.Duration {
	v, ok := f.values[key]
	if !ok || v == "" {
		return def
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		return time.Duration(n * float64(time.Second))
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	log.Printf("[config] %s=%q is not a duration; using %v", key, v, def)
	return def
}

// Size returns a byte size ("64KB", "16MB", "1GB"; a bare number is bytes).
func (f *File) Size(key string, def int64) int64 {
	v, ok := f.values[key]
	if !ok || v == "" {
		return def
	}
	n, err := ParseSize(v)
	if err != nil {
		log.Printf("[config] %s=%q: %v; using %d", key, v, err, def)
		return def
	}
	return n
}

// List returns a comma-separated list.
func (f *File) List(key string) []string {
	v := f.values[key]
	if v == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ParseSize parses "64KB", "16MB", "1.5GB", "4096".
func ParseSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	mult := float64(1)
	for _, u := range []struct {
		suffix string
		m      float64
	}{{"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(s, u.suffix) {
			mult = u.m
			s = strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			break
		}
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size")
	}
	return int64(n * mult), nil
}

// ===== runtime / resources =====

// ApplyRuntime applies process-wide resource limits:
//
//	MAX_PROCS        CPU cores Go may use (default: all)
//	MEMORY_LIMIT_MB  soft memory limit for the Go heap (default: none)
//	GC_PERCENT       garbage collector target (default 100)
func (f *File) ApplyRuntime() {
	if n := f.Int("MAX_PROCS", 0); n > 0 {
		runtime.GOMAXPROCS(n)
	}
	if mb := f.Int("MEMORY_LIMIT_MB", 0); mb > 0 {
		debug.SetMemoryLimit(int64(mb) << 20)
	}
	if p := f.Int("GC_PERCENT", 0); p != 0 {
		debug.SetGCPercent(p)
	}
}

// ===== mapping =====

// QUIC returns transport tuning shared by relays and nodes.
func (f *File) QUIC() transport.Config {
	q := transport.Config{
		MaxConnections:    uint32(f.Int("MAX_CONNECTIONS", 10000)),
		MaxStreamsPerConn: uint16(f.Int("MAX_STREAMS_PER_CONNECTION", 1024)),
		IdleTimeout:       f.Dur("IDLE_TIMEOUT", 60*time.Second),
		HandshakeTimeout:  f.Dur("HANDSHAKE_TIMEOUT", 10*time.Second),
		KeepAlive:         f.Dur("KEEPALIVE", 15*time.Second),
		Profile:           transport.ParseProfile(strings.ToLower(f.Str("EXECUTION_PROFILE", ""))),
		Congestion:        transport.ParseCongestion(strings.ToLower(f.Str("CONGESTION_CONTROL", ""))),
		DisableDatagrams:  !f.Bool("DATAGRAMS", true),
		CertHash:          f.Str("TLS_CERT_HASH", ""),
		CertMachineStore:  f.Bool("TLS_CERT_MACHINE_STORE", false),
	}
	// v1 names (MB) and v2 names (any size)
	q.SendWindow = uint32(f.Size("SEND_WINDOW", int64(f.Int("SEND_BUFFER_SIZE_MB", 8))<<20))
	q.RecvWindow = uint32(f.Size("RECV_WINDOW", int64(f.Int("RECV_BUFFER_SIZE_MB", 8))<<20))
	if f.Has("KEEPALIVE") && f.Dur("KEEPALIVE", 0) == 0 {
		q.KeepAlive = -1
	}
	return q
}

func (f *File) bindHost() string {
	if f.Bool("LOCALHOST_MODE", false) {
		return "127.0.0.1"
	}
	return f.Str("BIND_ADDRESS", "0.0.0.0")
}

// Relay maps settings onto a relay configuration.
func (f *File) Relay() relay.Config {
	listen := f.Str("RELAY_LISTEN", "")
	if listen == "" {
		listen = fmt.Sprintf("%s:%d", f.bindHost(), f.Int("RELAY_PORT", f.Int("TRANSPORT_PORT", 4002)))
	}
	legacy := f.Str("LEGACY_LISTEN", "")
	if legacy == "" && f.Bool("LEGACY_V1", true) {
		legacy = fmt.Sprintf("%s:%d", f.bindHost(), f.Int("LEGACY_PORT", 4003))
	}
	if strings.EqualFold(legacy, "off") {
		legacy = ""
	}
	return relay.Config{
		ID:                f.Str("RELAY_ID", ""),
		Listen:            listen,
		LegacyListen:      legacy,
		Mode:              netx.ParseMode(strings.ToLower(f.Str("TRANSPORT", f.Str("TRANSPORT_MODE", "auto")))),
		CertFile:          f.Str("TLS_CERT_FILE", ""),
		KeyFile:           f.Str("TLS_KEY_FILE", ""),
		CertHash:          f.Str("TLS_CERT_HASH", ""),
		QUIC:              f.QUIC(),
		AuthTokens:        f.List("AUTH_TOKENS"),
		IdentitiesFile:    f.Str("IDENTITIES_FILE", ""),
		RequireIdentity:   f.Bool("REQUIRE_IDENTITY", false),
		Peers:             f.List("RELAY_PEERS"),
		ClusterSecret:     f.Str("CLUSTER_SECRET", ""),
		MaxFrame:          int(f.Size("MAX_MESSAGE_SIZE", 16<<20)),
		MaxQueueBytes:     f.Size("MAX_QUEUE_PER_NODE", 32<<20),
		SlowConsumer:      relay.SlowConsumerPolicy(strings.ToLower(f.Str("SLOW_CONSUMER", "drop"))),
		MaxSessions:       f.Int("MAX_NODES", f.Int("MAX_CONNECTIONS", 100000)),
		MaxSubsPerSession: f.Int("MAX_SUBSCRIPTIONS_PER_NODE", 10000),
		MaxPipes:          f.Int("MAX_PIPES", 10000),
		PipeBufferBytes:   int(f.Size("PIPE_BUFFER", 256<<10)),
		OfflineMaxMsgs:    f.Int("OFFLINE_MAX_MESSAGES", 1000),
		OfflineMaxBytes:   f.Size("OFFLINE_MAX_BYTES", 256<<20),
		OfflineTTL:        f.Dur("OFFLINE_TTL", time.Hour),
		SessionTimeout:    f.Dur("SESSION_TIMEOUT", 45*time.Second),
		RateLimit:         f.Float("RATE_LIMIT", 0),
		RateBurst:         f.Int("RATE_BURST", 0),
	}
}

// Node maps settings onto a node configuration.
func (f *File) Node() node.Config {
	relays := f.List("RELAYS")
	if len(relays) == 0 {
		relays = f.List("RELAY_ADDRESSES")
	}
	if v := f.Str("RELAY_ADDRESS", ""); v != "" && v != "0.0.0.0:4001" && len(relays) == 0 {
		relays = []string{v}
	}
	route := node.Route(strings.ToLower(f.Str("ROUTE", "")))
	switch strings.ToLower(f.Str("RELAY_ASSISTED", "")) { // v1 name
	case "true":
		if route == "" {
			route = node.RouteRelay
		}
	case "false":
		if route == "" {
			route = node.RouteDirect
		}
	}
	listen := f.Str("DIRECT_LISTEN", "")
	if listen == "" {
		listen = fmt.Sprintf("%s:%d", f.bindHost(), f.Int("DIRECT_PORT", 0))
	}
	adv := f.List("ADVERTISE")
	if ip := f.Str("ADVERTISED_ADDRESS", ""); ip != "" && f.Int("DIRECT_PORT", 0) > 0 { // v1 name: bare IP
		adv = append(adv, fmt.Sprintf("%s:%d", ip, f.Int("DIRECT_PORT", 0)))
	}
	cs := f.Size("CHUNK_SIZE", int64(f.Int("CHUNK_SIZE_MB", 0))<<20)
	return node.Config{
		ID:                f.Str("NODE_ID", ""),
		Token:             f.Str("TOKEN", f.Str("AUTH_TOKEN", "")),
		Meta:              f.Str("NODE_META", ""),
		Relays:            relays,
		Transport:         netx.ParseMode(strings.ToLower(f.Str("TRANSPORT", f.Str("TRANSPORT_MODE", "auto")))),
		QUIC:              f.QUIC(),
		VerifyPeer:        !f.Bool("NO_CERT_VALIDATION", true),
		CAFile:            f.Str("TLS_CA_FILE", ""),
		CertFile:          f.Str("TLS_CERT_FILE", ""),
		KeyFile:           f.Str("TLS_KEY_FILE", ""),
		Listen:            f.Bool("DIRECT", false),
		ListenAddr:        listen,
		AdvertiseAddrs:    adv,
		UPnP:              f.Bool("UPNP", f.Bool("HOLE_PUNCH", false)),
		Route:             route,
		DirectTimeout:     f.Dur("DIRECT_TIMEOUT", 0),
		WatchPresence:     f.Bool("WATCH_PRESENCE", false),
		InboxSize:         f.Int("INBOX_SIZE", 0),
		SubBuffer:         f.Int("SUB_BUFFER", 0),
		SlowConsumerWait:  f.Dur("SLOW_CONSUMER_WAIT", 0),
		SendQueueBytes:    f.Size("SEND_QUEUE", 0),
		RequestTimeout:    f.Dur("REQUEST_TIMEOUT", 0),
		HeartbeatInterval: f.Dur("HEARTBEAT_INTERVAL", 0),
		HeartbeatTimeout:  f.Dur("HEARTBEAT_TIMEOUT", 0),
		ChunkSize:         cs,
		StreamsPerSource:  f.Int("STREAMS_PER_SOURCE", f.Int("MAX_PARALLEL_CHUNKS", 0)),
		Pipeline:          f.Int("PIPELINE", 0),
		NoCompression:     !f.Bool("COMPRESSION", true),
		CacheDir:          f.Str("CACHE_DIR", ""),
		LinkRelays:        f.List("LINK_RELAYS"),
	}
}

// WriteDefault writes the documented default settings file if path does
// not exist yet.
func WriteDefault(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(DefaultContent), 0o644)
}
