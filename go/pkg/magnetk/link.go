// Package magnetk parses and builds magnetk links.
//
//	magnetk:?xt=urn:sha256:<file sha256>
//	        &dn=<file name>
//	        &xl=<size in bytes>
//	        &cs=<chunk size>
//	        &mr=<manifest root: sha256 over the chunk hashes>
//	        &xs=<provider node id>
//	        &relay=<host:port>     (repeatable: several relays)
//	        &dht=true              (optional DHT discovery)
//
// The manifest root lets a downloader verify the chunk list it receives
// before trusting it, so every chunk can be checked as it arrives.
// Links produced by Magnetk v1 (relay given as a libp2p multiaddr such as
// /ip4/1.2.3.4/tcp/4001/p2p/12D3...) are still accepted; the host is kept
// and the v2 relay port is used.
package magnetk

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// DefaultRelayPort is the v2 relay port (QUIC/UDP and TCP).
const DefaultRelayPort = 4002

// MagnetLink is a parsed magnetk link.
type MagnetLink struct {
	FileHash     string   // xt (hex SHA-256 of the whole file)
	FileName     string   // dn
	FileSize     int64    // xl
	ChunkSize    int64    // cs
	ManifestRoot string   // mr
	SeedID       string   // xs (first provider node id)
	Relays       []string // relay (host:port), in preference order
	DHTEnabled   bool     // dht

	// RelayAddr is the first relay (kept for compatibility).
	RelayAddr string
}

// Parse parses a magnetk link.
func Parse(uri string) (*MagnetLink, error) {
	uri = strings.TrimSpace(uri)
	if !strings.HasPrefix(uri, "magnetk:?") {
		return nil, fmt.Errorf("invalid magnetk link: must start with 'magnetk:?'")
	}
	params, err := url.ParseQuery(strings.TrimPrefix(uri, "magnetk:?"))
	if err != nil {
		return nil, fmt.Errorf("invalid magnetk link: %w", err)
	}
	ml := &MagnetLink{}
	xt := params.Get("xt")
	if !strings.HasPrefix(xt, "urn:sha256:") {
		return nil, fmt.Errorf("invalid magnetk link: xt must be urn:sha256:<hash>")
	}
	ml.FileHash = strings.ToLower(strings.TrimPrefix(xt, "urn:sha256:"))
	if !isHex(ml.FileHash, 64) {
		return nil, fmt.Errorf("invalid magnetk link: file hash must be 64 hex characters")
	}
	ml.FileName = params.Get("dn")
	ml.SeedID = params.Get("xs")
	if v := params.Get("xl"); v != "" {
		ml.FileSize, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := params.Get("cs"); v != "" {
		ml.ChunkSize, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := strings.ToLower(params.Get("mr")); v != "" {
		if !isHex(v, 64) {
			return nil, fmt.Errorf("invalid magnetk link: mr must be 64 hex characters")
		}
		ml.ManifestRoot = v
	}
	for _, r := range params["relay"] {
		for _, one := range strings.Split(r, ",") {
			if addr := NormalizeRelay(one); addr != "" && !contains(ml.Relays, addr) {
				ml.Relays = append(ml.Relays, addr)
			}
		}
	}
	if len(ml.Relays) > 0 {
		ml.RelayAddr = ml.Relays[0]
	}
	d := strings.ToLower(params.Get("dht"))
	ml.DHTEnabled = d == "true" || d == "1"
	return ml, nil
}

// String renders the link.
func (ml *MagnetLink) String() string {
	var b strings.Builder
	b.WriteString("magnetk:?xt=urn:sha256:")
	b.WriteString(ml.FileHash)
	if ml.FileName != "" {
		b.WriteString("&dn=")
		b.WriteString(url.QueryEscape(ml.FileName))
	}
	if ml.FileSize > 0 {
		fmt.Fprintf(&b, "&xl=%d", ml.FileSize)
	}
	if ml.ChunkSize > 0 {
		fmt.Fprintf(&b, "&cs=%d", ml.ChunkSize)
	}
	if ml.ManifestRoot != "" {
		b.WriteString("&mr=")
		b.WriteString(ml.ManifestRoot)
	}
	if ml.SeedID != "" {
		b.WriteString("&xs=")
		b.WriteString(url.QueryEscape(ml.SeedID))
	}
	relays := ml.Relays
	if len(relays) == 0 && ml.RelayAddr != "" {
		relays = []string{ml.RelayAddr}
	}
	for _, r := range relays {
		b.WriteString("&relay=")
		b.WriteString(url.QueryEscape(r))
	}
	if ml.DHTEnabled {
		b.WriteString("&dht=true")
	}
	return b.String()
}

// Validate checks required fields.
func (ml *MagnetLink) Validate() error {
	if !isHex(ml.FileHash, 64) {
		return fmt.Errorf("file hash must be 64 hex characters")
	}
	return nil
}

// HasRelay reports whether the link names at least one relay.
func (ml *MagnetLink) HasRelay() bool { return len(ml.Relays) > 0 || ml.RelayAddr != "" }

// HasDHT reports whether DHT discovery is enabled.
func (ml *MagnetLink) HasDHT() bool { return ml.DHTEnabled }

// NormalizeRelay turns "host", "host:port", "quic://host:port" or a v1
// multiaddr into "host:port". Empty or unusable input returns "".
func NormalizeRelay(s string) string {
	s = strings.TrimSpace(s)
	for _, p := range []string{"quic://", "tcp://", "magnetk://"} {
		s = strings.TrimPrefix(s, p)
	}
	if s == "" || s == "0.0.0.0:4001" {
		return ""
	}
	if strings.HasPrefix(s, "/") { // v1 multiaddr: /ip4/HOST/tcp/PORT/p2p/ID
		parts := strings.Split(s, "/")
		if len(parts) >= 3 && (parts[1] == "ip4" || parts[1] == "ip6" || parts[1] == "dns" || parts[1] == "dns4" || parts[1] == "dns6") {
			return net.JoinHostPort(parts[2], strconv.Itoa(DefaultRelayPort))
		}
		return ""
	}
	if host, port, err := net.SplitHostPort(s); err == nil {
		if p, err := strconv.Atoi(port); err == nil && p == 4003 {
			// 4003 is the v1/legacy-only port; v2 clients use the relay port
			return net.JoinHostPort(host, strconv.Itoa(DefaultRelayPort))
		}
		return net.JoinHostPort(host, port)
	}
	return net.JoinHostPort(s, strconv.Itoa(DefaultRelayPort))
}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
