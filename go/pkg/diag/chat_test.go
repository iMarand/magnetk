package diag

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/iMarand/magnetk/go/pkg/chat"
	"github.com/iMarand/magnetk/go/pkg/netx"
)

// testWAV builds a 16 kHz mono 16-bit WAV of the given length.
func testWAV(ms int) []byte {
	n := 16000 * 2 * ms / 1000
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
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
	pcm := make([]byte, n)
	rand.Read(pcm)
	b.Write(pcm)
	return b.Bytes()
}

func TestChat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	h, err := StartHost(ctx, HostConfig{RelayListen: "127.0.0.1:0", DirectListen: "-", HTTP: "-", HTTPS: "-", FileSize: -1, DataDir: t.TempDir(), Out: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	got := make(chan chat.Msg, 16)
	c, err := NewClient(ClientConfig{ID: "phone", Relays: []string{fmt.Sprintf("127.0.0.1:%d", h.RelayPort())}, Transport: netx.ModeTCP, DataDir: t.TempDir(), OnChat: func(m chat.Msg) { got <- m }})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	next := func(kind string) chat.Msg {
		t.Helper()
		for {
			select {
			case m := <-got:
				if m.Kind == kind {
					return m
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("no %s message", kind)
			}
		}
	}

	// phone -> PC text, acked
	sent, err := c.ChatText("", "hello from the phone")
	if err != nil {
		t.Fatal(err)
	}
	if ack := next(chat.KindAck); ack.Text != sent.ID || ack.RTTMs <= 0 {
		t.Fatalf("bad ack %+v", ack)
	}
	waitFor(t, func() bool { return len(h.ChatHistory(0, 0)) == 1 })
	if m := h.ChatHistory(0, 0)[0]; m.From != "phone" || m.Text != "hello from the phone" || m.Dir != "in" || !m.Encrypted {
		t.Fatalf("host got %+v", m)
	}

	// PC -> phone text, default recipient = last peer
	if _, err := h.Chat(ctx, "", "hi phone"); err != nil {
		t.Fatal(err)
	}
	if m := next(chat.KindText); m.Text != "hi phone" || m.From != DefaultHostID || !m.Encrypted || m.Request {
		t.Fatalf("phone got %+v", m)
	}

	// voice notes both ways, verified and saved
	wav := testWAV(1500)
	if _, err := c.ChatAudio("", wav, "audio/wav", 0, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(h.ChatHistory(0, 0)) == 3 })
	in := h.ChatHistory(0, 0)[2]
	if in.Kind != chat.KindAudio || !in.Verified || in.DurMs != 1500 || in.Size != len(wav) {
		t.Fatalf("host audio %+v", in)
	}
	if b, _ := os.ReadFile(in.Path); !bytes.Equal(b, wav) {
		t.Fatal("saved voice note differs")
	}
	reply := testWAV(800)
	if _, err := h.ChatAudio(ctx, "phone", "audio/wav", 0, reply, "reply"); err != nil {
		t.Fatal(err)
	}
	m := next(chat.KindAudio)
	if !m.Verified || m.DurMs != 800 || m.Text != "reply" {
		t.Fatalf("phone audio %+v", m)
	}
	if b, _ := os.ReadFile(m.Path); !bytes.Equal(b, reply) {
		t.Fatal("phone's saved voice note differs")
	}

}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("timed out")
}
