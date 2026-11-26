// Package identity gives every Magnetk node a long-lived Ed25519 key.
//
// The key proves who a node is, on two levels:
//
//   - To the relay: the HELLO is signed, and the relay binds a node id to
//     the first key that claims it. Afterwards nobody else (not even
//     someone holding the relay token) can connect under that id.
//   - End to end: chat messages are signed, and receivers pin the sender's
//     key the first time they see it (like SSH known_hosts), or from a
//     magnetk://node@relay?key=<fingerprint> address (like an HTTPS
//     certificate, but without a certificate authority). A message whose
//     key does not match is flagged, even if the relay itself forged it.
//
// A fingerprint is the first 20 bytes of SHA-256(public key) in base32,
// e.g. "K7Q2M9XAPL3D5V8RWE4TNC6JHB2FYGQZ", shown as "K7Q2 M9XA PL3D ...".
package identity

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Key is a node's identity key pair.
type Key struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey

	xOnce sync.Once
	x     *ecdh.PrivateKey // encryption key derived from the seed (seal.go)
}

// Generate creates a new random key.
func Generate() *Key {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return &Key{priv: priv, pub: pub}
}

const fileHeader = "MAGNETK-ED25519-KEY v1"

// Load reads a key written by Save.
func Load(path string) (*Key, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r", ""), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != fileHeader {
		return nil, fmt.Errorf("identity: %s is not a magnetk key file", path)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("identity: %s: bad key", path)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return &Key{priv: priv, pub: priv.Public().(ed25519.PublicKey)}, nil
}

// Save writes the key (owner-only permissions).
func (k *Key) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body := fmt.Sprintf("%s\n%s\n# fingerprint %s - keep this file private: it is this node's identity\n",
		fileHeader, base64.StdEncoding.EncodeToString(k.priv.Seed()), k.Fingerprint())
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadOrCreate loads the key at path, creating and saving a new one if the
// file does not exist. created reports whether a new key was made.
func LoadOrCreate(path string) (k *Key, created bool, err error) {
	k, err = Load(path)
	if err == nil {
		return k, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	k = Generate()
	if err := k.Save(path); err != nil {
		return nil, false, err
	}
	return k, true, nil
}

// Public returns the public key (32 bytes).
func (k *Key) Public() []byte { return append([]byte(nil), k.pub...) }

// PublicString returns the public key in base64 (for JSON headers).
func (k *Key) PublicString() string { return base64.StdEncoding.EncodeToString(k.pub) }

// Fingerprint returns the key's fingerprint.
func (k *Key) Fingerprint() string { return Fingerprint(k.pub) }

// Sign signs msg.
func (k *Key) Sign(msg []byte) []byte { return ed25519.Sign(k.priv, msg) }

// Fingerprint returns the fingerprint of a public key ("" if invalid).
func Fingerprint(pub []byte) string {
	if len(pub) != ed25519.PublicKeySize {
		return ""
	}
	sum := sha256.Sum256(pub)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:20])
}

// Format groups a fingerprint for reading aloud: "K7Q2 M9XA PL3D ...".
func Format(fp string) string {
	var b strings.Builder
	for i, r := range fp {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Short is the first 8 characters, for compact UIs.
func Short(fp string) string {
	if len(fp) > 8 {
		return fp[:4] + "-" + fp[4:8]
	}
	return fp
}

// NormalizeFingerprint uppercases and strips spaces/dashes.
func NormalizeFingerprint(s string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "-", "", ":", "").Replace(strings.TrimSpace(s)))
}

// Verify checks sig over msg with a public key.
func Verify(pub, msg, sig []byte) bool {
	return len(pub) == ed25519.PublicKeySize && len(sig) == ed25519.SignatureSize && ed25519.Verify(pub, msg, sig)
}

// ParsePublic decodes a base64 public key.
func ParsePublic(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("identity: bad public key")
	}
	return b, nil
}

// HelloPayload is what a node signs when it connects to a relay: its id
// and the time, so a captured HELLO cannot be replayed later.
func HelloPayload(nodeID string, unixMs uint64) []byte {
	b := []byte("magnetk-hello-v1\x00" + nodeID + "\x00")
	return binary.BigEndian.AppendUint64(b, unixMs)
}

// ===== trust on first use =====

// Trust is the result of checking a peer's key.
type Trust string

const (
	TrustVerified Trust = "verified" // matches the pinned key
	TrustNew      Trust = "new"      // first contact: key pinned now
	TrustChanged  Trust = "changed"  // a different key than before: possibly someone else
	TrustUnsigned Trust = "unsigned" // peer sent no signature (old software)
	TrustInvalid  Trust = "invalid"  // signature does not verify: forged or corrupted
)

// Pins remembers which key belongs to which node id (known peers) and what
// the user decided about each contact: allowed (accepted a message request),
// blocked, verified (safety number compared).
type Pins struct {
	path string
	mu   sync.Mutex
	m    map[string]*Contact
}

// Contact is what is known about one peer.
type Contact struct {
	Key      string `json:"key"`                // pinned fingerprint
	Allowed  bool   `json:"allowed,omitempty"`  // may message us (approved, or we wrote first)
	Blocked  bool   `json:"blocked,omitempty"`  // messages are dropped
	Verified bool   `json:"verified,omitempty"` // safety number compared for this key
}

// OpenPins loads known peers from path ("" = memory only).
func OpenPins(path string) *Pins {
	p := &Pins{path: path, m: map[string]*Contact{}}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if json.Unmarshal(b, &p.m) != nil {
				// first format: node -> fingerprint
				var old map[string]string
				if json.Unmarshal(b, &old) == nil {
					for n, fp := range old {
						p.m[n] = &Contact{Key: fp}
					}
				}
			}
		}
	}
	return p
}

func (p *Pins) entry(node string) *Contact {
	c := p.m[node]
	if c == nil {
		c = &Contact{}
		p.m[node] = c
	}
	return c
}

// Check compares fp with the key pinned for node, pinning it on first use.
func (p *Pins) Check(node, fp string) Trust {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.m[node]
	switch {
	case c == nil || c.Key == "":
		p.entry(node).Key = fp
		p.saveLocked()
		return TrustNew
	case c.Key == fp:
		return TrustVerified
	default:
		return TrustChanged
	}
}

// Pin sets the expected key for node (e.g. from a key= address); it returns
// the previously pinned fingerprint if it was different. A changed key
// clears Verified.
func (p *Pins) Pin(node, fp string) (previous string) {
	fp = NormalizeFingerprint(fp)
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.entry(node)
	if c.Key != fp {
		previous = c.Key
		c.Verified = false
	}
	c.Key = fp
	p.saveLocked()
	return previous
}

// Get returns the pinned fingerprint for node.
func (p *Pins) Get(node string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.m[node]; c != nil {
		return c.Key
	}
	return ""
}

// Contact returns a copy of what is known about node.
func (p *Pins) Contact(node string) Contact {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.m[node]; c != nil {
		return *c
	}
	return Contact{}
}

// SetAllowed accepts (or un-accepts) messages from node; allowing unblocks.
func (p *Pins) SetAllowed(node string, allowed bool) {
	p.mu.Lock()
	c := p.entry(node)
	c.Allowed = allowed
	if allowed {
		c.Blocked = false
	}
	p.saveLocked()
	p.mu.Unlock()
}

// SetBlocked blocks (or unblocks) node.
func (p *Pins) SetBlocked(node string, blocked bool) {
	p.mu.Lock()
	c := p.entry(node)
	c.Blocked = blocked
	if blocked {
		c.Allowed = false
	}
	p.saveLocked()
	p.mu.Unlock()
}

// SetVerified marks node's current key as checked by safety number.
func (p *Pins) SetVerified(node string, verified bool) {
	p.mu.Lock()
	c := p.entry(node)
	c.Verified = verified && c.Key != ""
	p.saveLocked()
	p.mu.Unlock()
}

// Accept trusts node's new key after a KEY CHANGED warning (the user
// confirmed, e.g. the contact got a new phone). Verified is cleared.
func (p *Pins) Accept(node, fp string) {
	p.mu.Lock()
	c := p.entry(node)
	c.Key, c.Verified = NormalizeFingerprint(fp), false
	p.saveLocked()
	p.mu.Unlock()
}

func (p *Pins) saveLocked() {
	if p.path == "" {
		return
	}
	b, _ := json.MarshalIndent(p.m, "", "  ")
	os.MkdirAll(filepath.Dir(p.path), 0o700)
	tmp := p.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, p.path)
	}
}
