package identity

import (
	"path/filepath"
	"testing"
)

func TestKeyFileAndSignatures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "id.key")
	k, created, err := LoadOrCreate(path)
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	k2, created, err := LoadOrCreate(path)
	if err != nil || created || k2.Fingerprint() != k.Fingerprint() {
		t.Fatalf("reload: %v %v %s %s", created, err, k2.Fingerprint(), k.Fingerprint())
	}
	if len(k.Fingerprint()) != 32 {
		t.Fatalf("fingerprint %q", k.Fingerprint())
	}
	msg := HelloPayload("phone", 12345)
	sig := k.Sign(msg)
	if !Verify(k.Public(), msg, sig) {
		t.Fatal("valid signature rejected")
	}
	if Verify(k.Public(), HelloPayload("phone", 12346), sig) || Verify(Generate().Public(), msg, sig) {
		t.Fatal("bad signature accepted")
	}
	pub, err := ParsePublic(k.PublicString())
	if err != nil || Fingerprint(pub) != k.Fingerprint() {
		t.Fatal("public key round trip")
	}
	if NormalizeFingerprint(Format(k.Fingerprint())) != k.Fingerprint() {
		t.Fatal("format/normalize")
	}
}

func TestPins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	p := OpenPins(path)
	a, b := Generate().Fingerprint(), Generate().Fingerprint()
	if p.Check("alice", a) != TrustNew || p.Check("alice", a) != TrustVerified || p.Check("alice", b) != TrustChanged {
		t.Fatal("trust on first use")
	}
	// persisted
	if OpenPins(path).Check("alice", a) != TrustVerified {
		t.Fatal("pins not saved")
	}
	if prev := p.Pin("alice", Format(b)); prev != a || p.Check("alice", b) != TrustVerified {
		t.Fatal("explicit pin")
	}
}
