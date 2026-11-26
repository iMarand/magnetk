package chat

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/iMarand/magnetk/go/pkg/identity"
)

func TestEncodeDecode(t *testing.T) {
	att := []byte("voice bytes")
	m := New("phone", "pc", KindAudio, "caption", "audio/wav", 1200, att)
	b := Encode(m, att)
	if !IsChat(b) {
		t.Fatal("not recognised as chat")
	}
	got, gotAtt, err := Decode(b)
	if err != nil || got.ID != m.ID || got.Text != "caption" || got.DurMs != 1200 || !bytes.Equal(gotAtt, att) {
		t.Fatalf("decode: %+v %q %v", got, gotAtt, err)
	}
	if got.Dir != "" || got.Path != "" {
		t.Fatal("local fields were sent")
	}
	SaveAttachment("", &got, gotAtt)
	if !got.Verified {
		t.Fatal("good attachment not verified")
	}
	// a changed byte is detected
	bad := append([]byte(nil), gotAtt...)
	bad[0] ^= 1
	SaveAttachment("", &got, bad)
	if got.Verified {
		t.Fatal("tampered attachment verified")
	}
	if IsChat([]byte("Dxxxx")) || IsChat(nil) {
		t.Fatal("non-chat recognised")
	}
}

func TestWAVDuration(t *testing.T) {
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	n := 16000 * 2 * 3 // 3 s
	b.WriteString("RIFF")
	w(uint32(36 + n))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1))
	w(uint16(1))
	w(uint32(16000))
	w(uint32(32000))
	w(uint16(2))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(n))
	b.Write(make([]byte, n))
	if d := WAVDurationMs(b.Bytes()); d != 3000 {
		t.Fatalf("duration %d", d)
	}
	if WAVDurationMs([]byte("not a wav")) != 0 {
		t.Fatal("garbage has a duration")
	}
}

func TestSignatures(t *testing.T) {
	alice, mallory := identity.Generate(), identity.Generate()
	pins := identity.OpenPins("")
	m := New("alice", "bob", KindText, "hi", "", 0, nil)
	m.sign(alice)
	wire := Encode(m, nil)

	got, _, _ := Decode(wire)
	if got.verify(pins, "alice"); got.Trust != string(identity.TrustNew) || got.Fingerprint != alice.Fingerprint() {
		t.Fatalf("first contact: %s", got.Trust)
	}
	got, _, _ = Decode(wire)
	if got.verify(pins, "alice"); got.Trust != string(identity.TrustVerified) {
		t.Fatalf("second: %s", got.Trust)
	}
	// relay claims another sender
	got, _, _ = Decode(wire)
	if got.verify(pins, "eve"); got.Trust != string(identity.TrustInvalid) {
		t.Fatalf("wrong sender: %s", got.Trust)
	}
	// text changed in transit
	got, _, _ = Decode(wire)
	got.Text = "send money"
	if got.verify(pins, "alice"); got.Trust != string(identity.TrustInvalid) {
		t.Fatalf("tampered: %s", got.Trust)
	}
	// someone else signs as alice with their own key
	f := New("alice", "bob", KindText, "it's me", "", 0, nil)
	f.sign(mallory)
	if f.verify(pins, "alice"); f.Trust != string(identity.TrustChanged) {
		t.Fatalf("impostor: %s", f.Trust)
	}
	u := New("alice", "bob", KindText, "old app", "", 0, nil)
	if u.verify(pins, "alice"); u.Trust != string(identity.TrustUnsigned) {
		t.Fatalf("unsigned: %s", u.Trust)
	}
}
