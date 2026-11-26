package magnetk

// magnetk:// addresses name a node (or a relay) and optionally an action:
//
//	magnetk://<node>@<relay host[:port]>[/<action>][?<params>]
//	magnetk://<relay host[:port]>                       a relay
//	magnetk://a52-3cc9@192.168.1.66:4002                 node a52-3cc9 via that relay
//	magnetk://a52-3cc9@vps.example.com/chat?text=hello   chat with it (port 4002 by default)
//	magnetk://a52-3cc9@/chat                             use the relays from settings.conf
//
// Params: relay=<host:port> (repeatable, more relays), text=<chat text>,
// key=<identity fingerprint> (the node must prove it owns this key),
// token=<relay token> (optional; anyone who sees the link learns it).
//
// An address only says where and what: the program that opens it connects
// with the normal Magnetk protocol (TLS 1.3 over QUIC or TCP). File links
// keep their own form, magnetk:?xt=urn:sha256:... (see Parse).

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Scheme is the URI scheme of magnetk addresses and links.
const Scheme = "magnetk"

// Address is a parsed magnetk:// address.
type Address struct {
	Node   string     // node id; "" for a relay address
	Relays []string   // host:port, in preference order; empty = use settings
	Action string     // "" (open / chat), "chat", ...
	Text   string     // text= (chat message)
	Token  string     // token= (relay token)
	Key    string     // key= (identity fingerprint of the node: proves who it is)
	Params url.Values // all query parameters
}

// IsAddress reports whether s is a magnetk:// address (not a file link).
func IsAddress(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), Scheme+"://")
}

// IsLink reports whether s is a magnetk:? file link.
func IsLink(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), Scheme+":?")
}

// ParseAddress parses a magnetk:// address.
func ParseAddress(s string) (*Address, error) {
	s = strings.TrimSpace(s)
	if IsLink(s) {
		return nil, fmt.Errorf("%q is a file link, not an address", truncate(s))
	}
	if !IsAddress(s) {
		return nil, fmt.Errorf("invalid magnetk address %q: must start with magnetk://", truncate(s))
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("invalid magnetk address: %w", err)
	}
	a := &Address{Params: u.Query()}
	if u.User != nil {
		a.Node = u.User.Username()
	}
	if u.Host != "" {
		r, err := normRelay(u.Host)
		if err != nil {
			return nil, err
		}
		a.Relays = append(a.Relays, r)
	}
	for _, r := range a.Params["relay"] {
		nr, err := normRelay(r)
		if err != nil {
			return nil, err
		}
		a.Relays = append(a.Relays, nr)
	}
	a.Action = strings.Trim(u.Path, "/")
	a.Text = a.Params.Get("text")
	a.Token = a.Params.Get("token")
	a.Key = strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(a.Params.Get("key")))
	if a.Node == "" && len(a.Relays) == 0 {
		return nil, fmt.Errorf("magnetk address %q names neither a node nor a relay", truncate(s))
	}
	if a.Node == "" && a.Action != "" {
		return nil, fmt.Errorf("magnetk address %q: action %q needs a node (magnetk://node@relay/%s)", truncate(s), a.Action, a.Action)
	}
	return a, nil
}

// String formats the address (canonical form).
func (a *Address) String() string {
	u := url.URL{Scheme: Scheme, Host: "", Path: ""}
	if len(a.Relays) > 0 {
		u.Host = a.Relays[0]
	}
	if a.Node != "" {
		u.User = url.User(a.Node)
	}
	if a.Action != "" {
		u.Path = "/" + a.Action
	}
	q := url.Values{}
	for k, v := range a.Params {
		if k != "relay" && k != "text" && k != "token" && k != "key" {
			q[k] = v
		}
	}
	for _, r := range a.Relays[min(1, len(a.Relays)):] {
		q.Add("relay", r)
	}
	if a.Text != "" {
		q.Set("text", a.Text)
	}
	if a.Key != "" {
		q.Set("key", a.Key)
	}
	if a.Token != "" {
		q.Set("token", a.Token)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// NodeAddress returns the address of a node reachable through relays.
func NodeAddress(node string, relays ...string) string {
	return (&Address{Node: node, Relays: relays}).String()
}

// ChatAddress returns an address that opens a chat with node (text optional).
func ChatAddress(node, text string, relays ...string) string {
	return (&Address{Node: node, Relays: relays, Action: "chat", Text: text}).String()
}

func normRelay(h string) (string, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return "", fmt.Errorf("empty relay address")
	}
	if _, port, err := net.SplitHostPort(h); err == nil {
		if p, err := strconv.Atoi(port); err != nil || p <= 0 || p > 65535 {
			return "", fmt.Errorf("invalid relay port in %q", h)
		}
		return h, nil
	}
	if strings.Count(h, ":") > 1 && !strings.HasPrefix(h, "[") { // bare IPv6
		h = "[" + h + "]"
	}
	return net.JoinHostPort(strings.Trim(h, "[]"), strconv.Itoa(DefaultRelayPort)), nil
}

func truncate(s string) string {
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}
