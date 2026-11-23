package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iMarand/magnetk/go/pkg/files"
	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/protocol"
	"github.com/iMarand/magnetk/go/pkg/relay"
)

func startRelay(t testing.TB, cfg relay.Config) *relay.Server {
	t.Helper()
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	s, err := relay.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func addr(s *relay.Server) string { return fmt.Sprintf("127.0.0.1:%d", s.Port()) }

func startNode(t testing.TB, cfg Config) *Node {
	t.Helper()
	if cfg.CacheDir == "" {
		cfg.CacheDir = "-"
	}
	n, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := n.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	return n
}

func recv(t *testing.T, n *Node) *Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m, err := n.Receive(ctx)
	if err != nil {
		t.Fatalf("receive on %s: %v", n.ID(), err)
	}
	return m
}

// ctx10 returns a context that expires after 10s (its timer frees itself).
func ctx10() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	go func() { <-ctx.Done(); cancel() }()
	return ctx
}

func TestMessaging(t *testing.T) {
	r := startRelay(t, relay.Config{})
	a := startNode(t, Config{ID: "alice", Relays: []string{addr(r)}})
	b := startNode(t, Config{ID: "bob", Relays: []string{addr(r)}})

	// direct message + receipt
	if err := a.Send(ctx10(), "bob", []byte("hi bob"), WithReceipt()); err != nil {
		t.Fatal(err)
	}
	m := recv(t, b)
	if m.Kind != KindDirect || m.From != "alice" || string(m.Data) != "hi bob" {
		t.Fatalf("bad message %+v", m)
	}
	// no route
	if err := a.Send(ctx10(), "nobody", []byte("x"), WithReceipt()); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected ErrNoRoute, got %v", err)
	}

	// pub/sub with wildcard
	sub, err := b.Subscribe(ctx10(), "sensors.*.temp", nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	a.Publish(ctx10(), "sensors.kitchen.temp", []byte("21.5"))
	a.Publish(ctx10(), "sensors.kitchen.humidity", []byte("40")) // not matched
	m = recv(t, b)
	if m.Kind != KindPublish || m.Subject != "sensors.kitchen.temp" || string(m.Data) != "21.5" {
		t.Fatalf("bad publication %+v", m)
	}
	sub.Unsubscribe()

	// handler subscription keeps order
	var got []string
	var mu sync.Mutex
	done := make(chan struct{})
	b.Subscribe(ctx10(), "order", func(m *Message) {
		mu.Lock()
		got = append(got, string(m.Data))
		if len(got) == 100 {
			close(done)
		}
		mu.Unlock()
	})
	time.Sleep(50 * time.Millisecond)
	for i := 0; i < 100; i++ {
		a.Publish(ctx10(), "order", []byte(fmt.Sprint(i)))
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ordered messages not received")
	}
	for i, s := range got {
		if s != fmt.Sprint(i) {
			t.Fatalf("out of order at %d: %s", i, s)
		}
	}

	// request/reply to a node
	b.OnRequest(func(m *Message) ([]byte, error) {
		if string(m.Data) == "fail" {
			return nil, errors.New("asked to fail")
		}
		return append([]byte("echo:"), m.Data...), nil
	})
	resp, err := a.Request(ctx10(), "bob", []byte("ping"))
	if err != nil || string(resp) != "echo:ping" {
		t.Fatalf("request: %q %v", resp, err)
	}
	var re *RemoteError
	if _, err := a.Request(ctx10(), "bob", []byte("fail")); !errors.As(err, &re) || re.Msg != "asked to fail" {
		t.Fatalf("expected remote error, got %v", err)
	}
	if _, err := a.Request(ctx10(), "ghost", []byte("x")); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected no route, got %v", err)
	}

	// topic service with two workers in a queue group
	c := startNode(t, Config{ID: "carol", Relays: []string{addr(r)}})
	var served [2]atomic.Int32
	b.HandleRequests(ctx10(), "svc.add", func(m *Message) ([]byte, error) { served[0].Add(1); return []byte("b"), nil })
	c.HandleRequests(ctx10(), "svc.add", func(m *Message) ([]byte, error) { served[1].Add(1); return []byte("c"), nil })
	time.Sleep(100 * time.Millisecond)
	for i := 0; i < 40; i++ {
		if _, err := a.RequestTopic(ctx10(), "svc.add", []byte("1")); err != nil {
			t.Fatal(err)
		}
	}
	if served[0].Load() == 0 || served[1].Load() == 0 || served[0].Load()+served[1].Load() != 40 {
		t.Fatalf("load balancing: b=%d c=%d", served[0].Load(), served[1].Load())
	}

	// queue group publications: each message to exactly one member
	var qa, qb atomic.Int32
	b.QueueSubscribe(ctx10(), "jobs", "workers", func(*Message) { qa.Add(1) })
	c.QueueSubscribe(ctx10(), "jobs", "workers", func(*Message) { qb.Add(1) })
	time.Sleep(100 * time.Millisecond)
	for i := 0; i < 50; i++ {
		a.Publish(ctx10(), "jobs", []byte("j"))
	}
	waitFor(t, func() bool { return qa.Load()+qb.Load() == 50 })
	if qa.Load() == 0 || qb.Load() == 0 {
		t.Fatalf("queue group not balanced: %d/%d", qa.Load(), qb.Load())
	}

	// offline store
	if err := a.Send(ctx10(), "dave", []byte("for later"), WithStore(), WithReceipt()); err != nil {
		t.Fatal(err)
	}
	d := startNode(t, Config{ID: "dave", Relays: []string{addr(r)}})
	m = recv(t, d)
	if string(m.Data) != "for later" || m.From != "alice" {
		t.Fatalf("stored message: %+v", m)
	}

	// latency
	rtt, err := a.Ping(ctx10())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("relay round trip: %v (via %s)", rtt, a.Info().Protocol)
}

func testLogger(t testing.TB) *log.Logger {
	return log.New(testWriter{t}, "", log.Lmicroseconds)
}

type testWriter struct{ t testing.TB }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(bytes.TrimSpace(p)))
	return len(p), nil
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestPresenceAndLookup(t *testing.T) {
	r := startRelay(t, relay.Config{Logger: testLogger(t)})
	w := startNode(t, Config{ID: "watcher", Relays: []string{addr(r)}, WatchPresence: true})
	x := startNode(t, Config{ID: "x1", Relays: []string{addr(r)}, Meta: `{"role":"sensor"}`, Logger: testLogger(t)})
	m := recv(t, w)
	if m.Kind != KindPresence || m.From != "x1" || m.Subject != "join" || string(m.Data) != `{"role":"sensor"}` {
		t.Fatalf("join event: %+v", m)
	}
	info, err := w.Lookup(ctx10(), "x1")
	if err != nil || info.NodeID != "x1" {
		t.Fatalf("lookup: %+v %v", info, err)
	}
	list, _ := w.ListNodes(ctx10(), "")
	if len(list) != 2 {
		t.Fatalf("list: %d nodes", len(list))
	}
	t.Logf("closing x1 (connected=%v)", x.Connected())
	x.Close()
	t.Logf("x1 closed")
	ctxw, cw := context.WithTimeout(context.Background(), 3*time.Second)
	m, err = w.Receive(ctxw)
	cw()
	if err != nil {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		os.WriteFile(filepath.Join(os.TempDir(), "mk-stacks.txt"), buf[:n], 0o644)
		t.Fatalf("no leave event; stacks in %s", filepath.Join(os.TempDir(), "mk-stacks.txt"))
	}
	if m.Kind != KindPresence || m.Subject != "leave" {
		t.Fatalf("leave event: %+v", m)
	}
}

func TestAuth(t *testing.T) {
	r := startRelay(t, relay.Config{AuthTokens: []string{"s3cret"}})
	n, err := New(Config{Relays: []string{addr(r)}, Token: "wrong", CacheDir: "-"})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := n.Start(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
	ok := startNode(t, Config{Relays: []string{addr(r)}, Token: "s3cret"})
	if ok.ID() == "" {
		t.Fatal("no id assigned")
	}
}

func pipeEcho(t *testing.T, a, b *Node, size int, opts ...PipeOption) *Pipe {
	t.Helper()
	b.HandlePipe("echo", func(p *Pipe) {
		io.Copy(p, p)
		p.CloseWrite()
		p.Close()
	})
	p, err := a.OpenPipe(ctx10(), b.ID(), "echo", opts...)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	data := make([]byte, size)
	rand.Read(data)
	go func() {
		p.Write(data)
		p.CloseWrite()
	}()
	got, err := io.ReadAll(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("pipe echo mismatch: %d/%d bytes", len(got), len(data))
	}
	return p
}

func TestPipes(t *testing.T) {
	r := startRelay(t, relay.Config{})
	a := startNode(t, Config{ID: "pa", Relays: []string{addr(r)}})
	b := startNode(t, Config{ID: "pb", Relays: []string{addr(r)}, Listen: true, ListenAddr: "127.0.0.1:0"})

	p := pipeEcho(t, a, b, 8<<20, WithRoute(RouteRelay))
	if p.Direct {
		t.Fatal("relay route produced a direct pipe")
	}
	p = pipeEcho(t, a, b, 8<<20, WithRoute(RouteDirect))
	if !p.Direct {
		t.Fatal("direct route produced a relayed pipe")
	}
	p = pipeEcho(t, a, b, 1<<20) // auto -> direct
	if !p.Direct {
		t.Fatal("auto route should pick the direct path when reachable")
	}
	// auto falls back to relay when the target has no listener
	p = pipeEcho(t, b, a, 1<<20)
	if p.Direct {
		t.Fatal("expected relay fallback")
	}
	// unknown service
	if _, err := a.OpenPipe(ctx10(), "pb", "nope", WithRoute(RouteRelay)); err == nil {
		t.Fatal("expected rejection for unknown service")
	}
}

func makeFile(t testing.TB, dir string, size int) (string, []byte) {
	t.Helper()
	data := make([]byte, size)
	rand.Read(data)
	path := filepath.Join(dir, fmt.Sprintf("src-%d.bin", size))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, data
}

func checkFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the source (%d vs %d bytes)", path, len(got), len(want))
	}
}

func TestFiles(t *testing.T) {
	dir := t.TempDir()
	r := startRelay(t, relay.Config{})
	seed := startNode(t, Config{ID: "seed", Relays: []string{addr(r)}, Listen: true, ListenAddr: "127.0.0.1:0"})
	src, data := makeFile(t, dir, 24<<20+12345)

	sf, err := seed.Share(ctx10(), src, &ShareOptions{ChunkSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("link: %s", sf.Link)

	for _, route := range []Route{RouteRelay, RouteDirect, RouteAuto} {
		t.Run(string(route), func(t *testing.T) {
			dl := startNode(t, Config{Relays: []string{addr(r)}, Route: route})
			out := filepath.Join(dir, "out-"+string(route)+".bin")
			res, err := dl.Download(ctx10(), sf.Link, out, &DownloadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			checkFile(t, out, data)
			t.Logf("%s: %.1f MB/s", route, float64(res.Bytes)/res.Elapsed.Seconds()/1e6)
		})
	}

	// Multi-source: a downloader that seeds becomes a second provider.
	d1 := startNode(t, Config{ID: "d1", Relays: []string{addr(r)}})
	out1 := filepath.Join(dir, "d1.bin")
	if _, err := d1.Download(ctx10(), sf.Link, out1, &DownloadOptions{Seed: true}); err != nil {
		t.Fatal(err)
	}
	prov, err := d1.FindProviders(ctx10(), sf.Hash)
	if err != nil || len(prov.Nodes) != 2 {
		t.Fatalf("expected 2 providers, got %+v %v", prov, err)
	}
	d2 := startNode(t, Config{ID: "d2", Relays: []string{addr(r)}, Route: RouteRelay})
	out2 := filepath.Join(dir, "d2.bin")
	res, err := d2.Download(ctx10(), sf.Link, out2, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, out2, data)
	if len(res.PerSource) != 2 {
		t.Fatalf("expected chunks from 2 sources, got %v", res.PerSource)
	}
	t.Logf("multi-source split: %v", res.PerSource)

	// Resume: cancel half-way, then finish.
	d3 := startNode(t, Config{ID: "d3", Relays: []string{addr(r)}, Route: RouteRelay})
	out3 := filepath.Join(dir, "d3.bin")
	ctx, cancel := context.WithCancel(context.Background())
	_, err = d3.Download(ctx, sf.Link, out3, &DownloadOptions{StreamsPerSource: 1, Pipeline: 1, MaxSources: 1, Progress: func(p files.Progress) {
		if p.Done > int64(len(data))/3 {
			cancel()
		}
	}})
	if err == nil {
		t.Fatal("expected cancellation")
	}
	if _, err := os.Stat(out3 + ".mkpart.state"); err != nil {
		t.Fatalf("no resume state written: %v", err)
	}
	res, err = d3.Download(ctx10(), sf.Link, out3, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, out3, data)
	if res.Resumed == 0 {
		t.Fatal("download did not resume")
	}
	t.Logf("resumed %d bytes, fetched %d", res.Resumed, res.Bytes)

	// Unknown file
	if _, err := d3.Download(ctx10(), "0000000000000000000000000000000000000000000000000000000000000000", dir, nil); !errors.Is(err, ErrNoProviders) {
		t.Fatalf("expected ErrNoProviders, got %v", err)
	}
}

func TestFederation(t *testing.T) {
	dir := t.TempDir() // first: cleaned up after the nodes release their files
	secret := "cluster-secret"
	r1 := startRelay(t, relay.Config{ID: "relay-a", ClusterSecret: secret})
	r2 := startRelay(t, relay.Config{ID: "relay-b", ClusterSecret: secret, Peers: []string{addr(r1)}})
	// r1 also dials r2: the tie-break must leave exactly one link.
	r3 := startRelay(t, relay.Config{ID: "relay-c", ClusterSecret: "wrong", Peers: []string{addr(r1)}})
	_ = r3

	waitForLinks := func(s *relay.Server, n int) {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if len(s.Stats().Links) == n {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("%s has links %v, want %d", s.ID(), s.Stats().Links, n)
	}
	waitForLinks(r1, 1)
	waitForLinks(r2, 1)

	a := startNode(t, Config{ID: "on-a", Relays: []string{addr(r1)}})
	b := startNode(t, Config{ID: "on-b", Relays: []string{addr(r2)}})
	waitFor(t, func() bool { _, err := a.Lookup(ctx10(), "on-b"); return err == nil })

	if err := a.Send(ctx10(), "on-b", []byte("across"), WithReceipt()); err != nil {
		t.Fatal(err)
	}
	if m := recv(t, b); string(m.Data) != "across" || m.From != "on-a" {
		t.Fatalf("cross-relay message: %+v", m)
	}

	b.Subscribe(ctx10(), "news.>", nil)
	waitFor(t, func() bool {
		a.Publish(ctx10(), "news.world", []byte("hello"))
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		m, err := b.Receive(ctx)
		return err == nil && m.Subject == "news.world"
	})

	b.OnRequest(func(m *Message) ([]byte, error) { return []byte("pong from b"), nil })
	if resp, err := a.Request(ctx10(), "on-b", []byte("ping")); err != nil || string(resp) != "pong from b" {
		t.Fatalf("cross-relay request: %q %v", resp, err)
	}
	b.HandleRequests(ctx10(), "svc.time", func(m *Message) ([]byte, error) { return []byte("now"), nil })
	waitFor(t, func() bool {
		resp, err := a.RequestTopic(ctx10(), "svc.time", nil)
		return err == nil && string(resp) == "now"
	})

	pipeEcho(t, a, b, 4<<20, WithRoute(RouteRelay))

	src, data := makeFile(t, dir, 6<<20)
	sf, err := b.Share(ctx10(), src, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { p, err := a.FindProviders(ctx10(), sf.Hash); return err == nil && len(p.Nodes) == 1 })
	out := filepath.Join(dir, "fed.bin")
	if _, err := a.Download(ctx10(), sf.Link, out, &DownloadOptions{Route: RouteRelay}); err != nil {
		t.Fatal(err)
	}
	checkFile(t, out, data)
}

func TestFailover(t *testing.T) {
	r1 := startRelay(t, relay.Config{ID: "primary"})
	r2 := startRelay(t, relay.Config{ID: "backup"})
	var events []string
	var mu sync.Mutex
	n := startNode(t, Config{ID: "mobile", Relays: []string{addr(r1), addr(r2)}, ReconnectMin: 50 * time.Millisecond, Logger: testLogger(t),
		OnEvent: func(e Event) { mu.Lock(); events = append(events, string(e.Type)+":"+e.RelayID); mu.Unlock() }})
	peer := startNode(t, Config{ID: "fixed", Relays: []string{addr(r2)}})
	if n.Info().RelayID != "primary" {
		t.Fatalf("expected primary, got %s", n.Info().RelayID)
	}
	sub, _ := n.Subscribe(ctx10(), "alerts", nil)
	_ = sub
	r1.Close()
	waitFor(t, func() bool { return n.Info().RelayID == "backup" })
	// subscription restored on the backup relay
	waitFor(t, func() bool {
		peer.Publish(ctx10(), "alerts", []byte("still here"))
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		m, err := n.Receive(ctx)
		return err == nil && string(m.Data) == "still here"
	})
	mu.Lock()
	t.Logf("events: %v", events)
	mu.Unlock()
}

func TestTCPMode(t *testing.T) {
	r := startRelay(t, relay.Config{Mode: netx.ModeTCP})
	a := startNode(t, Config{ID: "ta", Relays: []string{addr(r)}, Transport: netx.ModeTCP})
	b := startNode(t, Config{ID: "tb", Relays: []string{addr(r)}}) // auto falls back to TCP
	if a.Info().Protocol != "tcp" || b.Info().Protocol != "tcp" {
		t.Fatalf("protocols: %s %s", a.Info().Protocol, b.Info().Protocol)
	}
	a.Send(ctx10(), "tb", []byte("over tcp"))
	if m := recv(t, b); string(m.Data) != "over tcp" {
		t.Fatalf("tcp message %+v", m)
	}
	pipeEcho(t, a, b, 4<<20, WithRoute(RouteRelay))
}

func TestDatagrams(t *testing.T) {
	r := startRelay(t, relay.Config{})
	if !r.QUICEnabled() {
		t.Skip("QUIC not available")
	}
	a := startNode(t, Config{ID: "da", Relays: []string{addr(r)}})
	b := startNode(t, Config{ID: "db", Relays: []string{addr(r)}})
	time.Sleep(200 * time.Millisecond) // datagram negotiation
	got := 0
	for i := 0; i < 20 && got == 0; i++ {
		a.Send(ctx10(), "db", []byte("fast"), Unreliable())
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		m, err := b.Receive(ctx)
		cancel()
		if err == nil && string(m.Data) == "fast" {
			got++
			if !m.Datagram {
				t.Fatal("expected datagram delivery")
			}
		}
	}
	if got == 0 {
		t.Fatal("no datagram delivered")
	}
}

// TestLegacyJSClient speaks Magnetk v1 framing to the relay's legacy port,
// as the JS SDK does: lookup + download through the relay proxy.
func TestLegacyJSClient(t *testing.T) {
	dir := t.TempDir()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	legacyAddr := ln.Addr().String()
	ln.Close()
	r := startRelay(t, relay.Config{LegacyListen: legacyAddr})
	seed := startNode(t, Config{ID: "jsseed", Relays: []string{addr(r)}})
	src, data := makeFile(t, dir, 3<<20+7)
	sf, err := seed.Share(ctx10(), src, nil)
	if err != nil {
		t.Fatal(err)
	}

	c, err := net.Dial("tcp", legacyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w, rd := protocol.NewFrameWriter(c), protocol.NewFrameReader(c)
	w.WriteJSON(protocol.MsgLookupPeer, protocol.LookupPeerPayload{FileHash: sf.Hash})
	f, err := rd.ReadFrame()
	if err != nil || f.Type != protocol.MsgLookupResult {
		t.Fatalf("lookup: %v %v", f, err)
	}
	var lr protocol.LookupResultPayload
	json.Unmarshal(f.Payload, &lr)
	if !lr.Found || len(lr.Seeds) != 1 || lr.Seeds[0].PeerID != "jsseed" {
		t.Fatalf("lookup result %+v", lr)
	}
	w.WriteJSON(protocol.MsgProxyConnect, protocol.ProxyConnectPayload{PeerID: "jsseed", FileHash: sf.Hash})
	if f, err = rd.ReadFrame(); err != nil || f.Type != protocol.MsgProxyReady {
		t.Fatalf("proxy: %v %v", f, err)
	}
	w.WriteJSON(protocol.MsgManifestReq, protocol.ManifestRequestPayload{FileHash: sf.Hash})
	f, err = rd.ReadFrame()
	if err != nil || f.Type != protocol.MsgManifestRes {
		t.Fatalf("manifest: %v", err)
	}
	var man protocol.ManifestResponsePayload
	json.Unmarshal(f.Payload, &man)
	idx := make([]uint32, man.TotalChunks)
	for i := range idx {
		idx[i] = uint32(i)
	}
	w.WriteJSON(protocol.MsgChunkReq, protocol.ChunkRequestPayload{FileHash: sf.Hash, ChunkIndices: idx})
	out := make([]byte, man.FileSize)
	for range idx {
		f, err = rd.ReadFrame()
		if err != nil || f.Type != protocol.MsgChunkRes {
			t.Fatalf("chunk: %v", err)
		}
		i, d, _ := protocol.ParseChunkPayload(f.Payload)
		copy(out[int64(i)*man.ChunkSize:], d)
	}
	if !bytes.Equal(out, data) {
		t.Fatal("legacy download mismatch")
	}
}

// TestSessionReplaced covers a reconnect the relay did not notice (the old
// connection is still open, as after a phone switched networks): the new
// session must replace the old one without a stale "leave" for watchers,
// without leaking the old session's subscriptions, and without the old
// session's cleanup removing the new session's files.
func TestSessionReplaced(t *testing.T) {
	dir := t.TempDir() // first, so it is removed after the nodes closed their files
	r := startRelay(t, relay.Config{})
	w := startNode(t, Config{ID: "watcher", Relays: []string{addr(r)}, WatchPresence: true})
	a1 := startNode(t, Config{ID: "phone", Relays: []string{addr(r)}, ReconnectMin: time.Minute, ReconnectMax: time.Minute})
	if m := recv(t, w); m.Kind != KindPresence || m.Subject != "join" || m.From != "phone" {
		t.Fatalf("join: %+v", m)
	}
	if _, err := a1.Subscribe(ctx10(), "old.topic", func(*Message) {}); err != nil {
		t.Fatal(err)
	}

	a2 := startNode(t, Config{ID: "phone", Relays: []string{addr(r)}, Listen: true, ListenAddr: "127.0.0.1:0"})
	a1.Close()
	if _, err := a2.Subscribe(ctx10(), "new.topic", func(*Message) {}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "f.bin")
	os.WriteFile(path, bytes.Repeat([]byte("x"), 100000), 0o644)
	sf, err := a2.Share(ctx10(), path, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if m, err := w.Receive(ctx); err == nil {
		t.Fatalf("unexpected event after replacement: %+v (%s)", m, m.Subject)
	}
	if n := r.Stats().Subscriptions; n != 1 {
		t.Fatalf("relay has %d subscriptions, want 1 (old session leaked)", n)
	}
	p, err := w.FindProviders(ctx10(), sf.Hash)
	if err != nil || len(p.Nodes) != 1 {
		t.Fatalf("providers after replacement: %+v %v", p, err)
	}
	if err := w.Send(ctx10(), "phone", []byte("still there?"), WithReceipt()); err != nil {
		t.Fatalf("send to replaced node: %v", err)
	}
}

// TestUnreachableRelaySkippedFast: a relay that never answers (e.g. a LAN
// relay while a phone is on mobile data) must not delay connecting through
// the next relay in the list.
func TestUnreachableRelaySkippedFast(t *testing.T) {
	r := startRelay(t, relay.Config{})
	t0 := time.Now()
	n := startNode(t, Config{Relays: []string{"10.255.255.1:4002", addr(r)}})
	t.Logf("connected after %v", time.Since(t0))
	if el := time.Since(t0); el > 3*time.Second {
		t.Fatalf("connected after %v; the unreachable relay was not skipped", el)
	}
	if in := n.Info(); in.Relay != addr(r) {
		t.Fatalf("connected to %q", in.Relay)
	}
}
