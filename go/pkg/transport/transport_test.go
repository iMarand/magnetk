//go:build cgo && !nomsquic

package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"runtime"
	"testing"
	"time"
)

func pair(t *testing.T) (*Transport, *Listener, *Transport) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.BindAddress = "127.0.0.1"
	cfg.Port = 0
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	l, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	cli, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close(); srv.Close() })
	return srv, l, cli
}

func TestEchoBulkDeadlinesDatagrams(t *testing.T) {
	_, l, cli := pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	serverErr := make(chan error, 1)
	go func() {
		conn, err := l.Accept(ctx)
		if err != nil {
			serverErr <- err
			return
		}
		// stream 1: echo until EOF
		s, err := conn.AcceptStream(ctx)
		if err != nil {
			serverErr <- err
			return
		}
		if _, err := io.Copy(s, s); err != nil {
			serverErr <- err
			return
		}
		s.CloseWrite()
		// datagram echo
		d, err := conn.ReceiveDatagram(ctx)
		if err != nil {
			serverErr <- err
			return
		}
		conn.SendDatagram(d)
		// server-initiated stream (bidirectional messaging needs this)
		ps, err := conn.OpenStream(ctx)
		if err != nil {
			serverErr <- err
			return
		}
		ps.Write([]byte("push"))
		ps.CloseWrite()
		<-conn.Done()
		serverErr <- nil
	}()

	conn, err := cli.Dial(ctx, "127.0.0.1", l.Port())
	if err != nil {
		t.Fatal(err)
	}

	s, err := conn.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// deadline on an idle read must fire
	s.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := s.Read(make([]byte, 10)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
	s.SetReadDeadline(time.Time{})

	payload := make([]byte, 32<<20+123)
	rand.Read(payload)
	go func() {
		s.Write(payload)
		s.CloseWrite()
	}()
	got, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo mismatch: got %d bytes want %d", len(got), len(payload))
	}
	s.Close()

	for i := 0; i < 100 && conn.MaxDatagram() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if err := conn.SendDatagram([]byte("dgram")); err != nil {
		t.Fatal(err)
	}
	d, err := conn.ReceiveDatagram(ctx)
	if err != nil || string(d) != "dgram" {
		t.Fatalf("datagram echo: %q %v", d, err)
	}

	ps, err := conn.AcceptStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	push, _ := io.ReadAll(ps)
	if string(push) != "push" {
		t.Fatalf("server push = %q", push)
	}
	ps.Close()

	st := conn.Stats()
	t.Logf("rtt=%v sent=%d lost=%d", st.RTT, st.SentBytes, st.LostPackets)
	conn.Close()
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestDialRefused(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Port = 0
	cli, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := cli.Dial(ctx, "127.0.0.1", 9); err == nil {
		t.Fatal("expected dial failure")
	}
	if time.Since(start) > 3500*time.Millisecond {
		t.Fatalf("dial failure took too long: %v", time.Since(start))
	}
}

func TestNoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 3; i++ {
		func() {
			_, l, cli := pair(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			go func() {
				c, err := l.Accept(ctx)
				if err != nil {
					return
				}
				s, err := c.AcceptStream(ctx)
				if err == nil {
					io.Copy(io.Discard, s)
					s.Close()
				}
			}()
			c, err := cli.Dial(ctx, "127.0.0.1", l.Port())
			if err != nil {
				t.Fatal(err)
			}
			s, _ := c.OpenStream(ctx)
			s.Write([]byte("x"))
			s.Close()
			c.Close()
			l.Close()
			cli.Close()
		}()
	}
	time.Sleep(200 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+4 {
		t.Fatalf("goroutines grew from %d to %d", before, after)
	}
}

func BenchmarkStreamThroughput(b *testing.B) {
	cfg := DefaultConfig()
	cfg.BindAddress = "127.0.0.1"
	cfg.Port = 0
	srv, _ := New(cfg)
	defer srv.Close()
	l, err := srv.Listen()
	if err != nil {
		b.Fatal(err)
	}
	cli, _ := New(cfg)
	defer cli.Close()
	ctx := context.Background()
	go func() {
		c, err := l.Accept(ctx)
		if err != nil {
			return
		}
		s, err := c.AcceptStream(ctx)
		if err != nil {
			return
		}
		io.Copy(io.Discard, s)
		s.Write([]byte{1})
		s.Close()
	}()
	c, err := cli.Dial(ctx, "127.0.0.1", l.Port())
	if err != nil {
		b.Fatal(err)
	}
	s, _ := c.OpenStream(ctx)
	buf := make([]byte, 256<<10)
	b.SetBytes(int64(len(buf)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Write(buf); err != nil {
			b.Fatal(err)
		}
	}
	s.CloseWrite()
	io.ReadAll(s)
	b.StopTimer()
	c.Close()
}
