package chat

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/iMarand/magnetk/go/pkg/hrtime"
	"github.com/iMarand/magnetk/go/pkg/identity"
	"github.com/iMarand/magnetk/go/pkg/node"
)

// keysPrefix starts a key-exchange request (answered inside the engine).
const keysPrefix = "MKCHATKEYS1"

// ErrKeyChanged is returned when sending to a peer whose key differs from
// the pinned one: accept the new key (AcceptKey) after checking it.
var ErrKeyChanged = errors.New("chat: the recipient's key changed - compare the safety number, then accept the new key")

// ErrNotEncrypted is returned with Policy.RequireEncryption when the peer
// cannot receive encrypted messages (old software or no identity key).
var ErrNotEncrypted = errors.New("chat: the recipient does not support end-to-end encryption")

// Policy controls what a box accepts.
type Policy struct {
	// RequireApproval delivers messages from senders that are not allowed
	// contacts as requests (Msg.Request, no ack) until Allow is called.
	// Writing to someone allows them.
	RequireApproval bool
	// RequireEncryption refuses to send unencrypted messages and marks
	// received unencrypted ones as requests.
	RequireEncryption bool
}

// Box is one endpoint's chat state: sending, encryption, acks, contacts,
// history and saved attachments.
type Box struct {
	self string
	dir  string // where received attachments are saved
	node func() *node.Node
	on   func(Msg) // new message, ack or send failure; must not block

	key    *identity.Key
	pins   *identity.Pins
	policy Policy

	mu       sync.Mutex
	sent     map[string]hrtime.Time // outgoing id -> send time
	hist     []Msg
	last     string            // peer of the last chat (default recipient)
	peerX    map[string]string // node -> verified X25519 key (base64)
	held     map[string][]string
	seenFP   map[string]string    // node -> last fingerprint seen (for AcceptKey)
	noKeys   map[string]time.Time // node -> when it last failed to answer the key exchange
	outFiles map[string]outgoingFile
}

// NewBox creates a box for node id self; attachments are saved in dir.
// on is called for every incoming message and ack, and must not block.
func NewBox(self, dir string, n func() *node.Node, on func(Msg)) *Box {
	return &Box{self: self, dir: dir, node: n, on: on, sent: map[string]hrtime.Time{},
		peerX: map[string]string{}, held: map[string][]string{}, seenFP: map[string]string{}, noKeys: map[string]time.Time{}, outFiles: map[string]outgoingFile{}}
}

// SetIdentity makes the box sign and encrypt with key and check what it
// receives against pins (known contacts). Without it messages are plain.
func (b *Box) SetIdentity(key *identity.Key, pins *identity.Pins) {
	if pins == nil {
		pins = identity.OpenPins("")
	}
	b.mu.Lock()
	b.key, b.pins = key, pins
	b.mu.Unlock()
}

// SetPolicy sets what the box accepts.
func (b *Box) SetPolicy(p Policy) {
	b.mu.Lock()
	b.policy = p
	b.mu.Unlock()
}

// Attach registers the key-exchange responder on the node. Call it once
// the node exists (before or after Start).
func (b *Box) Attach(n *node.Node) {
	n.HandleSystemRequests(keysPrefix, b.answerKeys)
}

// Pins returns the contacts store.
func (b *Box) Pins() *identity.Pins { return b.pins }

// ===== contacts =====

// Allow accepts messages from peer (a message request) and acknowledges
// the messages that were held.
func (b *Box) Allow(peer string) {
	b.pins.SetAllowed(peer, true)
	b.mu.Lock()
	ids := b.held[peer]
	delete(b.held, peer)
	b.mu.Unlock()
	for _, id := range ids {
		if m, ok := b.Get(id); ok && m.Kind == KindFile && m.Path == "" {
			m.Request = false
			go b.fetchFile(m) // acks once the file arrived
			continue
		}
		b.sendAck(peer, id)
	}
}

// Block drops everything from peer from now on.
func (b *Box) Block(peer string) { b.pins.SetBlocked(peer, true) }

// MarkVerified records that the safety number with peer was compared.
func (b *Box) MarkVerified(peer string) { b.pins.SetVerified(peer, true) }

// AcceptKey trusts peer's new key after a KEY CHANGED warning.
func (b *Box) AcceptKey(peer string) error {
	b.mu.Lock()
	fp := b.seenFP[peer]
	b.mu.Unlock()
	if fp == "" {
		return errors.New("chat: no new key seen for " + peer)
	}
	b.pins.Accept(peer, fp)
	return nil
}

// SafetyNumber returns the number to compare with peer (both sides see the
// same 60 digits), or "" if peer's key is not known yet.
func (b *Box) SafetyNumber(peer string) string {
	fp := b.pins.Get(peer)
	if fp == "" || b.key == nil {
		return ""
	}
	return identity.SafetyNumber(b.key.Fingerprint(), fp)
}

// Contact returns what is known about peer.
func (b *Box) Contact(peer string) identity.Contact { return b.pins.Contact(peer) }

func (b *Box) add(m Msg) {
	b.hist = append(b.hist, m)
	if len(b.hist) > 2000 {
		b.hist = b.hist[len(b.hist)-2000:]
	}
}

// ===== key exchange =====

func (b *Box) bundle(to string) []byte {
	m := Msg{ID: newID(), From: b.self, To: to, Kind: kindKeys, T: time.Now().UnixMilli()}
	m.sign(b.key)
	return Encode(m, nil)
}

// learn records peer's encryption key from a verified, signed message.
func (b *Box) learn(peer string, m Msg) {
	if m.XKey == "" {
		return
	}
	switch identity.Trust(m.Trust) {
	case identity.TrustNew, identity.TrustVerified:
		b.mu.Lock()
		b.peerX[peer] = m.XKey
		b.mu.Unlock()
	}
}

func (b *Box) answerKeys(req *node.Message) ([]byte, error) {
	if b.key == nil {
		return nil, errors.New("no identity key")
	}
	if m, _, err := Decode(req.Data[len(keysPrefix):]); err == nil && m.Kind == kindKeys {
		m.verify(b.pins, req.From)
		b.learn(req.From, m)
	}
	return b.bundle(req.From), nil
}

// peerKey returns peer's encryption key, asking peer for it if needed.
func (b *Box) peerKey(ctx context.Context, n *node.Node, peer string) (string, error) {
	b.mu.Lock()
	x := b.peerX[peer]
	failed, tried := b.noKeys[peer]
	b.mu.Unlock()
	if x != "" {
		return x, nil
	}
	if tried && time.Since(failed) < time.Minute {
		return "", ErrNotEncrypted // old peer: do not wait again for a minute
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := n.Request(ctx, peer, append([]byte(keysPrefix), b.bundle(peer)...))
	if err != nil {
		b.mu.Lock()
		b.noKeys[peer] = time.Now()
		b.mu.Unlock()
		return "", ErrNotEncrypted
	}
	m, _, err := Decode(resp)
	if err != nil || m.Kind != kindKeys {
		return "", ErrNotEncrypted
	}
	m.verify(b.pins, peer)
	switch identity.Trust(m.Trust) {
	case identity.TrustChanged:
		b.mu.Lock()
		b.seenFP[peer] = m.Fingerprint
		b.mu.Unlock()
		return "", ErrKeyChanged
	case identity.TrustNew, identity.TrustVerified:
		b.learn(peer, m)
		return m.XKey, nil
	}
	return "", ErrNotEncrypted
}

func sealAAD(m Msg) []byte {
	return []byte("magnetk-seal-v1|" + m.ID + "|" + m.From + "|" + m.To + "|" + strconv.FormatInt(m.T, 10))
}

// ===== sending =====

// Send sends a text (att == nil) or a voice note to node "to" (default: the
// last peer that chatted), end-to-end encrypted when both sides have
// identity keys. It returns once the relay confirmed delivery.
func (b *Box) Send(ctx context.Context, to, text, mime string, durMs int, att []byte) (Msg, error) {
	n := b.node()
	if n == nil {
		return Msg{}, errors.New("not connected")
	}
	if len(att) > InlineLimit {
		// big attachments go through the file engine: chunked, resumable,
		// peer to peer when possible, and they do not block other messages
		f, err := os.CreateTemp(b.outboxDir(), "att-*"+extFor(mime))
		if err != nil {
			return Msg{}, err
		}
		_, err = f.Write(att)
		f.Close()
		defer os.Remove(f.Name())
		if err != nil {
			return Msg{}, err
		}
		return b.SendFile(ctx, to, f.Name(), text, mime, durMs)
	}
	b.mu.Lock()
	if to == "" {
		to = b.last
	}
	policy := b.policy
	b.mu.Unlock()
	if to == "" {
		return Msg{}, errors.New("no recipient yet")
	}
	kind := KindText
	if len(att) > 0 {
		kind = KindAudio
		if mime == "" {
			mime = "audio/wav"
		}
	}
	m := New(b.self, to, kind, text, mime, durMs, att)
	wire, err := b.wrap(ctx, n, &m, att, policy)
	if err != nil {
		return m, err
	}
	if b.pins != nil {
		b.pins.SetAllowed(to, true) // writing to someone accepts their replies
	}
	b.mu.Lock()
	b.sent[m.ID] = hrtime.Now()
	b.last = to
	b.add(m) // before sending: the ack can arrive before Send returns
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := n.Send(ctx, to, wire, node.WithReceipt()); err != nil {
		b.mu.Lock()
		delete(b.sent, m.ID)
		for i := len(b.hist) - 1; i >= 0; i-- {
			if b.hist[i].ID == m.ID {
				b.hist = append(b.hist[:i], b.hist[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		return m, err
	}
	return m, nil
}

// wrap returns the wire form of m: sealed for the recipient when possible.
func (b *Box) wrap(ctx context.Context, n *node.Node, m *Msg, att []byte, policy Policy) ([]byte, error) {
	if b.key == nil {
		if policy.RequireEncryption {
			return nil, ErrNotEncrypted
		}
		return Encode(*m, att), nil
	}
	x, err := b.peerKey(ctx, n, m.To)
	if err != nil {
		if errors.Is(err, ErrKeyChanged) || policy.RequireEncryption {
			return nil, err
		}
		m.sign(b.key) // old peer: signed, not encrypted
		return Encode(*m, att), nil
	}
	rx, err := identity.ParseXPublic(x)
	if err != nil {
		return nil, err
	}
	inner := *m
	inner.sign(b.key)
	outer := Msg{ID: m.ID, From: m.From, To: m.To, Kind: kindSealed, T: m.T}
	epk, ct, err := b.key.Seal(rx, sealAAD(outer), Encode(inner, att))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(ct)
	outer.EPK, outer.Size, outer.SHA256 = base64.StdEncoding.EncodeToString(epk), len(ct), hex.EncodeToString(sum[:])
	outer.sign(b.key)
	m.Encrypted, m.Key, m.Sig = true, inner.Key, inner.Sig
	return Encode(outer, ct), nil
}

// ===== receiving =====

// Handle processes an incoming chat message from "from".
func (b *Box) Handle(from string, data []byte) {
	m, att, err := Decode(data)
	if err != nil {
		return
	}
	m.verify(b.pins, from)
	m.From, m.Dir = from, "in" // the relay-authenticated sender wins
	b.learn(from, m)
	if m.Trust == string(identity.TrustChanged) {
		b.mu.Lock()
		b.seenFP[from] = m.Fingerprint
		b.mu.Unlock()
	}
	contact := identity.Contact{}
	if b.pins != nil {
		contact = b.pins.Contact(from)
	}
	if contact.Blocked {
		return
	}
	if m.Kind == kindSealed {
		inner, iatt, err := b.open(m, att)
		if err != nil {
			m.Error = err.Error()
			b.on(m)
			return
		}
		m, att = inner, iatt
	}
	if m.Kind == kindKeys {
		return
	}
	m.SafetyChecked = contact.Verified && m.Trust == string(identity.TrustVerified)
	if m.Kind == KindAck {
		if m.Trust == string(identity.TrustInvalid) || m.Trust == string(identity.TrustChanged) {
			b.on(m) // reported, but it does not confirm delivery
			return
		}
		b.dropOutgoing(m.Text) // a sent file was received: stop sharing it
		b.mu.Lock()
		if t0, ok := b.sent[m.Text]; ok {
			m.RTTMs = round2(float64(hrtime.Since(t0).Microseconds()) / 1000)
			delete(b.sent, m.Text)
			for i := len(b.hist) - 1; i >= 0; i-- {
				if b.hist[i].ID == m.Text {
					b.hist[i].Acked, b.hist[i].RTTMs = true, m.RTTMs
					break
				}
			}
		}
		b.mu.Unlock()
		b.on(m)
		return
	}
	b.mu.Lock()
	policy := b.policy
	b.mu.Unlock()
	switch identity.Trust(m.Trust) {
	case identity.TrustChanged, identity.TrustInvalid:
		m.Request = true // never accepted silently
	}
	if policy.RequireApproval && !contact.Allowed {
		m.Request = true
	}
	if policy.RequireEncryption && !m.Encrypted {
		m.Request = true
	}
	if m.Kind == KindFile {
		b.mu.Lock()
		b.add(m)
		if m.Request {
			b.held[from] = append(b.held[from], m.ID)
		} else {
			b.last = from
		}
		b.mu.Unlock()
		if m.Request {
			b.on(m) // shown as a request; fetched only after Allow
		} else {
			go b.fetchFile(m)
		}
		return
	}
	SaveAttachment(b.dir, &m, att)
	b.mu.Lock()
	b.add(m)
	if !m.Request {
		b.last = from
	} else {
		b.held[from] = append(b.held[from], m.ID)
	}
	b.mu.Unlock()
	b.on(m)
	if !m.Request {
		b.sendAck(from, m.ID)
	}
}

// open decrypts a sealed envelope and checks that it matches its header.
func (b *Box) open(outer Msg, ct []byte) (Msg, []byte, error) {
	if outer.Trust == string(identity.TrustInvalid) || outer.Trust == string(identity.TrustUnsigned) {
		return Msg{}, nil, errors.New("sealed message without a valid signature")
	}
	if b.key == nil {
		return Msg{}, nil, errors.New("this device has no identity key: cannot decrypt")
	}
	sx, err1 := identity.ParseXPublic(outer.XKey)
	epk, err2 := base64.StdEncoding.DecodeString(outer.EPK)
	sum := sha256.Sum256(ct)
	if err1 != nil || err2 != nil || hex.EncodeToString(sum[:]) != outer.SHA256 {
		return Msg{}, nil, errors.New("sealed message is malformed or was modified")
	}
	pt, err := b.key.Open(sx, epk, sealAAD(outer), ct)
	if err != nil {
		return Msg{}, nil, err
	}
	inner, att, err := Decode(pt)
	if err != nil || inner.ID != outer.ID || inner.From != outer.From || inner.To != outer.To {
		return Msg{}, nil, errors.New("sealed message content does not match its envelope")
	}
	inner.From, inner.Dir = outer.From, "in"
	inner.Trust, inner.Fingerprint, inner.Encrypted = outer.Trust, outer.Fingerprint, true
	return inner, att, nil
}

func (b *Box) sendAck(to, id string) {
	n := b.node()
	if n == nil {
		return
	}
	am := Msg{ID: newID(), From: b.self, To: to, Kind: KindAck, Text: id, T: time.Now().UnixMilli()}
	am.sign(b.key)
	ack := Encode(am, nil)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		n.Send(ctx, to, ack)
	}()
}
