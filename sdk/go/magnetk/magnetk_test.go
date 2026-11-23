package magnetk_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iMarand/magnetk/sdk/go/magnetk"
)

func startRelay(t *testing.T) string {
	t.Helper()
	r, err := magnetk.StartRelay(magnetk.RelayConfig{Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r.Addr()
}

func connect(t *testing.T, ctx context.Context, cfg magnetk.Config) *magnetk.Node {
	t.Helper()
	n, err := magnetk.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	return n
}

func TestSDK(t *testing.T) {
	dir := t.TempDir()
	addr := startRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	a := connect(t, ctx, magnetk.Config{Relays: []string{addr}, ID: "alice", CacheDir: "-"})
	b := connect(t, ctx, magnetk.Config{Relays: []string{addr}, ID: "bob", Listen: true, CacheDir: "-"})

	// direct message with receipt
	if err := a.Send(ctx, "bob", []byte("hi bob"), magnetk.WithReceipt()); err != nil {
		t.Fatal(err)
	}
	m, err := b.Receive(ctx)
	if err != nil || string(m.Data) != "hi bob" || m.From != "alice" || m.Kind != magnetk.KindDirect {
		t.Fatalf("receive: %v %+v", err, m)
	}

	// pub/sub with wildcard
	got := make(chan string, 1)
	if _, err := b.Subscribe(ctx, "news.>", func(m *magnetk.Message) { got <- m.Subject + "=" + string(m.Data) }); err != nil {
		t.Fatal(err)
	}
	if err := a.Publish(ctx, "news.tech.go", []byte("1.22")); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if s != "news.tech.go=1.22" {
			t.Fatal(s)
		}
	case <-ctx.Done():
		t.Fatal("publish not received")
	}

	// request/reply via service topic
	if _, err := b.HandleRequests(ctx, "svc.echo", func(m *magnetk.Message) ([]byte, error) {
		return append([]byte("echo:"), m.Data...), nil
	}); err != nil {
		t.Fatal(err)
	}
	r, err := a.RequestTopic(ctx, "svc.echo", []byte("x"))
	if err != nil || string(r) != "echo:x" {
		t.Fatalf("request: %v %q", err, r)
	}

	// pipes, both routes
	b.HandlePipe("upper", func(p *magnetk.Pipe) {
		defer p.Close()
		data, _ := io.ReadAll(p)
		p.Write(bytes.ToUpper(data))
	})
	for _, route := range []magnetk.Route{magnetk.RouteRelay, magnetk.RouteDirect} {
		p, err := a.OpenPipe(ctx, "bob", "upper", magnetk.WithRoute(route))
		if err != nil {
			t.Fatalf("%s pipe: %v", route, err)
		}
		p.Write([]byte("abc"))
		p.CloseWrite()
		out, _ := io.ReadAll(p)
		p.Close()
		if string(out) != "ABC" || p.Direct != (route == magnetk.RouteDirect) {
			t.Fatalf("%s pipe: %q direct=%v", route, out, p.Direct)
		}
	}

	// files
	src := filepath.Join(dir, "data.bin")
	payload := make([]byte, 3<<20+123)
	rand.Read(payload)
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	sf, err := b.Share(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	l, err := magnetk.ParseLink(sf.Link)
	if err != nil || l.FileHash != sf.Hash {
		t.Fatalf("link: %v", err)
	}
	res, err := a.Download(ctx, sf.Link, filepath.Join(dir, "out.bin"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := magnetk.VerifyFile(res.Path, sf.Hash); err != nil {
		t.Fatal(err)
	}
}

func TestConnectNoRelays(t *testing.T) {
	if _, err := magnetk.Connect(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}
