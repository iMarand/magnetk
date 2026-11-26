package chat

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iMarand/magnetk/go/pkg/identity"
	"github.com/iMarand/magnetk/go/pkg/relay"
)

func TestEncryptedFileFormat(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	rand.Read(key)
	text := []byte(strings.Repeat("compressible line of text 12345\n", 70000))
	for _, size := range []int{0, 1, segSize - 1, segSize, segSize + 1, 3*segSize + 17} {
		data := make([]byte, size)
		rand.Read(data)
		for _, d := range [][]byte{data, text[:min(size, len(text))]} {
			src, enc, out := filepath.Join(dir, "src"), filepath.Join(dir, "enc"), filepath.Join(dir, "out")
			os.WriteFile(src, d, 0o644)
			sum, err := encryptFile(src, enc, key)
			if err != nil {
				t.Fatalf("size %d: %v", size, err)
			}
			if got, _ := fileSHA256(src); got != sum {
				t.Fatal("wrong plaintext hash")
			}
			if err := decryptFile(enc, out, key); err != nil {
				t.Fatalf("size %d: decrypt: %v", size, err)
			}
			if got, _ := os.ReadFile(out); !bytes.Equal(got, d) {
				t.Fatalf("size %d: round trip differs", size)
			}
			ct, _ := os.ReadFile(enc)
			if len(d) > 1000 && bytes.Contains(ct, d[100:200]) {
				t.Fatal("plaintext visible in the encrypted file")
			}
			if len(ct) > 40 {
				bad := append([]byte(nil), ct...)
				bad[len(bad)/2] ^= 1
				os.WriteFile(enc, bad, 0o600)
				if decryptFile(enc, out, key) == nil {
					t.Fatalf("size %d: modified file accepted", size)
				}
				if len(ct) > 17+segSize+16 { // drop the last segment: truncation must be detected
					os.WriteFile(enc, ct[:17+segSize+16], 0o600)
					if decryptFile(enc, out, key) == nil {
						t.Fatalf("size %d: truncated file accepted", size)
					}
				}
			}
		}
	}
	// compressible content shrinks
	src, enc := filepath.Join(dir, "t"), filepath.Join(dir, "t.enc")
	os.WriteFile(src, text, 0o644)
	encryptFile(src, enc, key)
	if st, _ := os.Stat(enc); st.Size() > int64(len(text))/2 {
		t.Fatalf("compressible file not compressed: %d -> %d", len(text), st.Size())
	}
}

func TestSendFileEndToEnd(t *testing.T) {
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
	alice := newPeer(t, addr, "f-alice", identity.Generate(), Policy{}, nil)
	bob := newPeer(t, addr, "f-bob", identity.Generate(), Policy{}, spy)
	ctx := context.Background()

	// a 3 MB voice note goes through the file engine automatically
	voice := make([]byte, 3<<20)
	rand.Read(voice)
	sent, err := alice.box.Send(ctx, "f-bob", "long voice note", "audio/mp4", 95000, voice)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Kind != KindFile || !sent.Encrypted || sent.Link == "" {
		t.Fatalf("sent %+v", sent)
	}
	if w := <-spy; bytes.Contains(w, []byte(sent.FileKey)) || bytes.Contains(w, []byte("long voice note")) {
		t.Fatal("file key or caption visible to the relay")
	}
	var got Msg
	deadline := time.After(30 * time.Second)
	for got.Path == "" {
		select {
		case m := <-bob.got:
			if m.Kind == KindFile && !m.Pending {
				got = m
			}
		case <-deadline:
			t.Fatal("file not received")
		}
	}
	if !got.Verified || !got.Encrypted || got.DurMs != 95000 || got.Text != "long voice note" || got.Error != "" {
		t.Fatalf("bob got %+v", got)
	}
	if b, _ := os.ReadFile(got.Path); !bytes.Equal(b, voice) {
		t.Fatal("received file differs")
	}
	if a := alice.next(t, KindAck); a.Text != sent.ID {
		t.Fatalf("ack %+v", a)
	}
	// once acknowledged, alice stops sharing it
	waitUntil(t, func() bool { return len(alice.n.SharedFiles()) == 0 })
}

func waitUntil(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("timed out")
}
