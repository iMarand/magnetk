package netx

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func echoServer(t *testing.T, l Listener) {
	go func() {
		for {
			c, err := l.Accept(context.Background())
			if err != nil {
				return
			}
			go func(c Conn) {
				for {
					s, err := c.AcceptStream(context.Background())
					if err != nil {
						return
					}
					go func(s Stream) {
						io.Copy(s, s)
						s.CloseWrite()
						s.Close()
					}(s)
				}
			}(c)
		}
	}()
}

func roundTrip(t *testing.T, c Conn, size int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := c.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	data := make([]byte, size)
	rand.Read(data)
	go func() {
		s.Write(data)
		s.CloseWrite()
	}()
	got, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("mismatch: %d vs %d bytes", len(got), len(data))
	}
}

func TestModes(t *testing.T) {
	var legacyHit = make(chan string, 1)
	srv, err := NewEndpoint(Options{Mode: ModeAuto, Legacy: func(c net.Conn) {
		buf := make([]byte, 5)
		io.ReadFull(c, buf)
		legacyHit <- string(buf)
		c.Close()
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	l, err := srv.Listen("127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	echoServer(t, l)
	addr := fmt.Sprintf("127.0.0.1:%d", l.Port())

	for _, mode := range []Mode{ModeTCP, ModeQUIC, ModeAuto} {
		t.Run(string(mode), func(t *testing.T) {
			cli, err := NewEndpoint(Options{Mode: mode})
			if err != nil {
				if mode == ModeQUIC {
					t.Skip("QUIC unavailable:", err)
				}
				t.Fatal(err)
			}
			defer cli.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c, err := cli.Dial(ctx, addr)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			t.Logf("connected via %s rtt=%v", c.Protocol(), c.RTT())
			if mode == ModeTCP && c.Protocol() != "tcp" {
				t.Fatalf("expected tcp, got %s", c.Protocol())
			}
			if mode == ModeQUIC && c.Protocol() != "quic" {
				t.Fatalf("expected quic, got %s", c.Protocol())
			}
			roundTrip(t, c, 4<<20)
			// several concurrent streams on one connection
			done := make(chan struct{}, 8)
			for i := 0; i < 8; i++ {
				go func() { roundTrip(t, c, 256<<10); done <- struct{}{} }()
			}
			for i := 0; i < 8; i++ {
				<-done
			}
		})
	}

	// Raw (non-TLS) TCP goes to the legacy handler.
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	raw.Write([]byte{0, 0, 0, 0, 0x32})
	select {
	case got := <-legacyHit:
		if got != string([]byte{0, 0, 0, 0, 0x32}) {
			t.Fatalf("legacy payload %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("legacy handler not called")
	}
	raw.Close()
}

func TestAutoFallsBackToTCP(t *testing.T) {
	// TCP-only server: an auto client must still connect (via TCP).
	srv, err := NewEndpoint(Options{Mode: ModeTCP})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	l, err := srv.Listen("127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	echoServer(t, l)
	cli, err := NewEndpoint(Options{Mode: ModeAuto, TCPFallbackDelay: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	start := time.Now()
	c, err := cli.Dial(context.Background(), fmt.Sprintf("127.0.0.1:%d", l.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Protocol() != "tcp" {
		t.Fatalf("expected tcp fallback, got %s", c.Protocol())
	}
	t.Logf("fallback connect took %v", time.Since(start))
	roundTrip(t, c, 1<<20)
}
