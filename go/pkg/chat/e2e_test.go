package chat

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iMarand/magnetk/go/pkg/identity"
	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/node"
	"github.com/iMarand/magnetk/go/pkg/relay"
)

type peer struct {
	n   *node.Node
	box *Box
	got chan Msg
}

// spy captures every chat payload that reaches a node, as the relay sees it.
func newPeer(t *testing.T, relayAddr, id string, key *identity.Key, policy Policy, spy chan []byte) *peer {
	t.Helper()
	n, err := node.New(node.Config{ID: id, Relays: []string{relayAddr}, Transport: netx.ModeTCP, CacheDir: "-", Key: key})
	if err != nil {
		t.Fatal(err)
	}
	p := &peer{n: n, got: make(chan Msg, 64)}
	p.box = NewBox(id, t.TempDir(), func() *node.Node { return n }, func(m Msg) { p.got <- m })
	if key != nil {
		p.box.SetIdentity(key, identity.OpenPins(""))
		p.box.Attach(n)
	}
	p.box.SetPolicy(policy)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := n.Start(ctx); err != nil {
		t.Fatal(err)
	}
	go func() {
		for m := range n.Messages() {
			if m.Kind == node.KindDirect && IsChat(m.Data) {
				if spy != nil {
					spy <- append([]byte(nil), m.Data...)
				}
				p.box.Handle(m.From, m.Data)
			}
		}
	}()
	t.Cleanup(func() { n.Close() })
	return p
}

func (p *peer) next(t *testing.T, kind string) Msg {
	t.Helper()
	for {
		select {
		case m := <-p.got:
			if m.Kind == kind {
				return m
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no %s message", kind)
		}
	}
}

func TestEndToEnd(t *testing.T) {
	r, err := relay.New(relay.Config{Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", r.Port())
	spy := make(chan []byte, 64)
	alice := newPeer(t, addr, "alice", identity.Generate(), Policy{}, nil)
	bob := newPeer(t, addr, "bob", identity.Generate(), Policy{RequireApproval: true}, spy)
	ctx := context.Background()

	// 1. encrypted text; bob has approval on, so it is a request (no ack)
	if _, err := alice.box.Send(ctx, "bob", "meet at 7, secret", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	wire := <-spy
	if bytes.Contains(wire, []byte("secret")) || bytes.Contains(wire, []byte("meet")) {
		t.Fatal("plaintext visible on the wire")
	}
	m := bob.next(t, KindText)
	if !m.Encrypted || m.Text != "meet at 7, secret" || !m.Request || m.Trust != string(identity.TrustVerified) {
		t.Fatalf("bob got %+v", m)
	}
	select {
	case a := <-alice.got:
		t.Fatalf("ack before approval: %+v", a)
	case <-time.After(300 * time.Millisecond):
	}
	// 2. bob accepts the request: alice gets the held ack
	bob.box.Allow("alice")
	if a := alice.next(t, KindAck); a.Trust != string(identity.TrustVerified) {
		t.Fatalf("ack %+v", a)
	}
	// 3. encrypted voice note back, verified end to end
	voice := bytes.Repeat([]byte("RIFFpcm"), 5000)
	if _, err := bob.box.Send(ctx, "alice", "", "audio/wav", 900, voice); err != nil {
		t.Fatal(err)
	}
	v := alice.next(t, KindAudio)
	if !v.Encrypted || !v.Verified || v.Size != len(voice) || v.Request {
		t.Fatalf("alice voice %+v", v)
	}
	// 4. both sides see the same safety number
	sa, sb := alice.box.SafetyNumber("bob"), bob.box.SafetyNumber("alice")
	if sa == "" || sa != sb || len(sa) != 71 {
		t.Fatalf("safety numbers %q vs %q", sa, sb)
	}
	alice.box.MarkVerified("bob")
	if _, err := bob.box.Send(ctx, "alice", "checked?", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	if c := alice.next(t, KindText); !c.SafetyChecked || !c.Encrypted {
		t.Fatalf("after verification %+v", c)
	}
	// 5. a peer without an identity key: signed-only fallback, or refused
	carol := newPeer(t, addr, "carol", nil, Policy{}, nil)
	if _, err := alice.box.Send(ctx, "carol", "hi carol", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	if c := carol.next(t, KindText); c.Encrypted || c.Text != "hi carol" {
		t.Fatalf("carol %+v", c)
	}
	alice.box.SetPolicy(Policy{RequireEncryption: true})
	if _, err := alice.box.Send(ctx, "carol", "no", "", 0, nil); err != ErrNotEncrypted {
		t.Fatalf("require encryption: %v", err)
	}
	// 6. blocked senders are dropped
	bob.box.Block("alice")
	alice.box.SetPolicy(Policy{})
	alice.box.Send(ctx, "bob", "are you there", "", 0, nil)
	deadline := time.After(500 * time.Millisecond)
	for done := false; !done; {
		select {
		case m := <-bob.got:
			if m.Kind != KindAck {
				t.Fatalf("blocked message delivered: %+v", m)
			}
		case <-deadline:
			done = true
		}
	}
}

func TestSealOpen(t *testing.T) {
	a, b, eve := identity.Generate(), identity.Generate(), identity.Generate()
	epk, ct, err := a.Seal(b.XPublic(), []byte("aad"), []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if pt, err := b.Open(a.XPublic(), epk, []byte("aad"), ct); err != nil || string(pt) != "hello" {
		t.Fatalf("open: %q %v", pt, err)
	}
	if _, err := eve.Open(a.XPublic(), epk, []byte("aad"), ct); err == nil {
		t.Fatal("someone else decrypted it")
	}
	if _, err := b.Open(eve.XPublic(), epk, []byte("aad"), ct); err == nil {
		t.Fatal("wrong sender accepted")
	}
	if _, err := b.Open(a.XPublic(), epk, []byte("other"), ct); err == nil {
		t.Fatal("aad not bound")
	}
	ct[0] ^= 1
	if _, err := b.Open(a.XPublic(), epk, []byte("aad"), ct); err == nil {
		t.Fatal("modified ciphertext accepted")
	}
}
