// Package chat is Magnetk's secure chat: text messages and voice notes (or
// any attachment) sent as direct messages, end-to-end encrypted and signed
// with the participants' identity keys, each answered with an ack so the
// sender sees delivery and the real round trip. The Android app, the diag
// host, "magnetk chat" and the SDKs all speak it.
//
// Security (see docs/SECURITY.md):
//   - every message is signed (Ed25519) and checked against the sender's
//     pinned key: forged, changed or impersonated messages are flagged;
//   - text and attachments are sealed for the recipient (X25519 + AES-256-GCM):
//     the relay sees only who talks to whom, when, and the size;
//   - unknown senders arrive as message requests until allowed (Policy);
//   - contacts can be verified by comparing a safety number.
//
// Wire format of one chat message (direct message body):
//
//	'C' <JSON Msg> '\n' <attachment bytes>
package chat

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iMarand/magnetk/go/pkg/identity"
)

// Message kinds.
const (
	KindText  = "text"
	KindAudio = "audio"
	KindAck   = "ack"

	kindSealed = "sealed" // wire only: encrypted envelope
	kindKeys   = "keys"   // wire only: key exchange
)

// MaxAttachment bounds one voice note (about 6 minutes of 16 kHz mono WAV).
const MaxAttachment = 12 << 20

// Tag is the first byte of every chat message.
const Tag = 'C'

// Msg is one chat entry.
type Msg struct {
	ID     string `json:"id"`
	From   string `json:"from"`
	To     string `json:"to,omitempty"`
	Kind   string `json:"kind"`             // text, audio, ack
	Text   string `json:"text,omitempty"`   // message text; for an ack, the acknowledged id
	Mime   string `json:"mime,omitempty"`   // attachment type, e.g. audio/wav
	DurMs  int    `json:"dur_ms,omitempty"` // voice note length
	Size   int    `json:"size,omitempty"`   // attachment bytes
	SHA256 string `json:"sha256,omitempty"` // attachment hash (hex)
	T      int64  `json:"t"`                // sender clock, unix ms
	Key    string `json:"key,omitempty"`    // sender's Ed25519 public key (base64)
	Sig    string `json:"sig,omitempty"`    // signature over Signed() (base64)
	XKey   string `json:"xkey,omitempty"`   // sender's X25519 encryption key (base64)
	EPK    string `json:"epk,omitempty"`    // sealed: ephemeral X25519 key (base64)
	// file attachments (KindFile): the file travels through the file engine
	Name    string `json:"name,omitempty"`     // file name
	Link    string `json:"link,omitempty"`     // magnetk:? link of the (encrypted) file
	FileKey string `json:"file_key,omitempty"` // AES key of the encrypted file (inside sealed messages only)

	// Filled in locally, never sent.
	Dir      string  `json:"dir,omitempty"`      // "in" or "out"
	Path     string  `json:"path,omitempty"`     // saved attachment
	Verified bool    `json:"verified,omitempty"` // attachment hash matched
	RTTMs    float64 `json:"rtt_ms,omitempty"`   // ack: send -> ack round trip
	Acked    bool    `json:"acked,omitempty"`
	// Trust is the result of checking the signature against the sender's
	// pinned key (identity.Trust: verified, new, changed, unsigned, invalid).
	Trust       string `json:"trust,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"` // sender key fingerprint
	// Encrypted: the message was end-to-end encrypted.
	Encrypted bool `json:"encrypted,omitempty"`
	// Request: the sender is not an allowed contact yet (no ack was sent);
	// call Box.Allow or Box.Block.
	Request bool `json:"request,omitempty"`
	// SafetyChecked: the sender's key was verified with the safety number.
	SafetyChecked bool   `json:"safety_checked,omitempty"`
	Error         string `json:"error,omitempty"` // e.g. cannot decrypt
	// Pending: a file attachment is being downloaded (a final event follows).
	Pending bool `json:"pending,omitempty"`
}

// Signed returns the bytes a chat message signature covers: every field
// that carries meaning, including the attachment hash.
func (m Msg) Signed() []byte {
	return []byte(fmt.Sprintf("magnetk-chat-v3|%q|%q|%q|%q|%q|%q|%d|%d|%q|%d|%q|%q|%q|%q|%q",
		m.ID, m.From, m.To, m.Kind, m.Text, m.Mime, m.DurMs, m.Size, m.SHA256, m.T, m.XKey, m.EPK, m.Name, m.Link, m.FileKey))
}

func (m *Msg) sign(k *identity.Key) {
	if k == nil {
		return
	}
	m.Key, m.XKey = k.PublicString(), k.XPublicString()
	m.Sig = base64.StdEncoding.EncodeToString(k.Sign(m.Signed()))
}

// verify checks the signature; claimedFrom must equal the relay-reported sender.
func (m *Msg) verify(pins *identity.Pins, from string) {
	if m.Sig == "" || m.Key == "" {
		m.Trust = string(identity.TrustUnsigned)
		return
	}
	pub, err := identity.ParsePublic(m.Key)
	sig, err2 := base64.StdEncoding.DecodeString(m.Sig)
	if err != nil || err2 != nil || m.From != from || !identity.Verify(pub, m.Signed(), sig) {
		m.Trust = string(identity.TrustInvalid)
		return
	}
	m.Fingerprint = identity.Fingerprint(pub)
	if pins == nil {
		m.Trust = string(identity.TrustNew)
		return
	}
	m.Trust = string(pins.Check(from, m.Fingerprint))
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Encode builds the wire form of m with its attachment.
func Encode(m Msg, att []byte) []byte {
	m.Dir, m.Path, m.Verified, m.RTTMs, m.Acked, m.Trust, m.Fingerprint = "", "", false, 0, false, "", ""
	m.Encrypted, m.Request, m.SafetyChecked, m.Error, m.Pending = false, false, false, "", false
	h, _ := json.Marshal(m)
	out := make([]byte, 0, 2+len(h)+len(att))
	out = append(out, Tag)
	out = append(out, h...)
	out = append(out, '\n')
	return append(out, att...)
}

// IsChat reports whether a direct message body is a chat message.
func IsChat(b []byte) bool { return len(b) > 1 && b[0] == Tag && b[1] == '{' }

// Decode splits a chat message into header and attachment.
func Decode(b []byte) (Msg, []byte, error) {
	var m Msg
	if !IsChat(b) {
		return m, nil, errors.New("not a chat message")
	}
	i := bytes.IndexByte(b, '\n')
	if i < 0 {
		return m, nil, errors.New("chat: no header end")
	}
	if err := json.Unmarshal(b[1:i], &m); err != nil {
		return m, nil, err
	}
	return m, b[i+1:], nil
}

// New builds an outgoing message (with hash and size of att).
func New(from, to, kind, text, mime string, durMs int, att []byte) Msg {
	m := Msg{ID: newID(), From: from, To: to, Kind: kind, Text: text, Mime: mime, DurMs: durMs, T: time.Now().UnixMilli(), Dir: "out"}
	if len(att) > 0 {
		sum := sha256.Sum256(att)
		m.Size, m.SHA256 = len(att), hex.EncodeToString(sum[:])
	}
	return m
}

// SaveAttachment verifies att against the header and writes it to dir.
func SaveAttachment(dir string, m *Msg, att []byte) {
	if len(att) == 0 {
		return
	}
	sum := sha256.Sum256(att)
	m.Verified = hex.EncodeToString(sum[:]) == m.SHA256 && len(att) == m.Size
	if dir == "" {
		return
	}
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, safeName(m.From)+"-"+safeName(m.ID)+extFor(m.Mime))
	if os.WriteFile(p, att, 0o644) == nil {
		m.Path = p
	}
}

func extFor(mime string) string {
	switch {
	case strings.Contains(mime, "wav"):
		return ".wav"
	case strings.Contains(mime, "mp4"), strings.Contains(mime, "m4a"), strings.Contains(mime, "aac"):
		return ".m4a"
	case strings.Contains(mime, "ogg"), strings.Contains(mime, "opus"):
		return ".ogg"
	case strings.Contains(mime, "mpeg"), strings.Contains(mime, "mp3"):
		return ".mp3"
	}
	return ".bin"
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return '_'
	}, s)
}

// Summary is a one-line description for logs.
func Summary(m Msg) string {
	switch m.Kind {
	case KindAudio:
		s := "voice note " + fmtDur(m.DurMs) + ", " + fmtSize(m.Size)
		if m.Dir == "in" {
			if m.Verified {
				s += ", sha256 ok"
			} else {
				s += ", SHA256 MISMATCH"
			}
		}
		return s
	case KindAck:
		return "ack " + m.Text
	case KindFile:
		s := "file " + m.Name + ", " + fmtSize(m.Size)
		if m.DurMs > 0 {
			s = "voice note " + fmtDur(m.DurMs) + ", " + fmtSize(m.Size) + " (file transfer)"
		}
		if m.Dir == "in" && !m.Pending {
			if m.Verified {
				s += ", sha256 ok"
			} else if m.Path != "" {
				s += ", SHA256 MISMATCH"
			}
		}
		return s
	}
	return m.Text
}

// TrustLabel describes m.Trust for people.
func TrustLabel(m Msg) string {
	switch identity.Trust(m.Trust) {
	case identity.TrustVerified:
		if m.SafetyChecked {
			return "verified " + identity.Short(m.Fingerprint) + ", safety number checked"
		}
		return "verified " + identity.Short(m.Fingerprint)
	case identity.TrustNew:
		return "new key " + identity.Short(m.Fingerprint) + " (saved)"
	case identity.TrustChanged:
		return "WARNING: KEY CHANGED (" + identity.Short(m.Fingerprint) + ") - not the sender you talked to before"
	case identity.TrustInvalid:
		return "WARNING: INVALID SIGNATURE - forged or corrupted"
	case identity.TrustUnsigned:
		return "unsigned (old app)"
	}
	return ""
}

func fmtDur(ms int) string { return fmt.Sprintf("%.1fs", float64(ms)/1000) }

func fmtSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// History returns chat entries newer than sinceMs (unix ms), at most n.
func (b *Box) History(sinceMs int64, n int) []Msg {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Msg
	for _, m := range b.hist {
		if m.T > sinceMs {
			out = append(out, m)
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// Get returns the entry with the given id.
func (b *Box) Get(id string) (Msg, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.hist) - 1; i >= 0; i-- {
		if b.hist[i].ID == id {
			return b.hist[i], true
		}
	}
	return Msg{}, false
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// WAVDurationMs reads the duration of a PCM WAV file (0 if unknown).
func WAVDurationMs(b []byte) int {
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return 0
	}
	var byteRate uint32
	for i := 12; i+8 <= len(b); {
		id, size := string(b[i:i+4]), le32(b[i+4:])
		body := i + 8
		switch id {
		case "fmt ":
			if body+12 <= len(b) {
				byteRate = le32(b[body+8:])
			}
		case "data":
			if byteRate == 0 {
				return 0
			}
			if int(size) > len(b)-body {
				size = uint32(len(b) - body)
			}
			return int(uint64(size) * 1000 / uint64(byteRate))
		}
		i = body + int(size) + int(size&1)
	}
	return 0
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
