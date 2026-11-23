package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iMarand/magnetk/go/pkg/hrtime"
	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/node"
	"github.com/iMarand/magnetk/go/pkg/relay"
)

type benchResult struct {
	Name   string             `json:"name"`
	Values map[string]float64 `json:"values"`
	Notes  string             `json:"notes,omitempty"`
}

// BenchMain runs benchmarks between two in-process nodes.
func BenchMain(args []string) error {
	nf := newNodeFlags("bench")
	local := nf.fs.Bool("local", false, "start an embedded relay on 127.0.0.1 (no setup needed)")
	count := nf.fs.Int("n", 100000, "messages for msg bench / requests for req bench (req uses n/20)")
	size := nf.fs.Int("size", 128, "message size in bytes")
	conc := nf.fs.Int("c", 16, "concurrent requesters for the req throughput test")
	mb := nf.fs.Int("mb", 256, "megabytes for pipe and file benches")
	jsonOut := nf.fs.Bool("json", false, "print results as JSON")
	if err := nf.parse(args); err != nil {
		return err
	}
	what := "all"
	if nf.fs.NArg() > 0 {
		what = nf.fs.Arg(0)
	}

	var relays []string
	if *local {
		mode := netx.ParseMode(*nf.transport)
		srv, err := relay.New(relay.Config{Listen: "127.0.0.1:0", Mode: mode, MaxQueueBytes: 256 << 20})
		if err != nil {
			return err
		}
		if err := srv.Start(); err != nil {
			return err
		}
		defer srv.Close()
		relays = []string{fmt.Sprintf("127.0.0.1:%d", srv.Port())}
		*nf.relays = relays[0]
	}

	mk := func(id string, listen bool) (*node.Node, error) {
		cfg, err := nf.nodeConfig(relays)
		if err != nil {
			return nil, err
		}
		cfg.ID = id
		cfg.Listen = listen
		if listen {
			cfg.ListenAddr = "127.0.0.1:0"
			if !*local {
				cfg.ListenAddr = "0.0.0.0:0"
			}
		}
		cfg.InboxSize = 1 << 17
		cfg.SubBuffer = 1 << 17
		cfg.SendQueueBytes = 64 << 20
		cfg.CacheDir = "-"
		n, err := node.New(cfg)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), *nf.timeout)
		defer cancel()
		if err := n.Start(ctx); err != nil {
			n.Close()
			return nil, err
		}
		return n, nil
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%100000)
	a, err := mk("bench-a-"+suffix, false)
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := mk("bench-b-"+suffix, true)
	if err != nil {
		return err
	}
	defer b.Close()

	if !*jsonOut {
		fmt.Printf("Magnetk bench: relay %s via %s, payload %d B\n\n", a.Info().Relay, a.Info().Protocol, *size)
	}
	var results []benchResult
	report := func(r benchResult) {
		results = append(results, r)
		if *jsonOut {
			return
		}
		keys := make([]string, 0, len(r.Values))
		for k := range r.Values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf("%-26s", r.Name)
		for _, k := range keys {
			fmt.Printf("  %s=%.2f", k, r.Values[k])
		}
		if r.Notes != "" {
			fmt.Printf("  (%s)", r.Notes)
		}
		fmt.Println()
	}

	run := func(name string) bool { return what == "all" || what == name }
	if run("msg") {
		r, err := benchPubSub(a, b, *count, *size)
		if err != nil {
			return fmt.Errorf("msg bench: %w", err)
		}
		report(r)
	}
	if run("req") {
		r, err := benchRequests(a, b, max(*count/20, 100), *size, *conc)
		if err != nil {
			return fmt.Errorf("req bench: %w", err)
		}
		for _, x := range r {
			report(x)
		}
	}
	if run("pipe") {
		for _, route := range []node.Route{node.RouteRelay, node.RouteDirect} {
			r, err := benchPipe(a, b, route, int64(*mb)<<20)
			if err != nil {
				report(benchResult{Name: "pipe/" + string(route), Notes: "unavailable: " + err.Error()})
				continue
			}
			report(r)
		}
	}
	if run("file") {
		for _, route := range []node.Route{node.RouteRelay, node.RouteDirect} {
			r, err := benchFile(a, b, route, int64(*mb)<<20)
			if err != nil {
				report(benchResult{Name: "file/" + string(route), Notes: "unavailable: " + err.Error()})
				continue
			}
			report(r)
		}
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}
	return nil
}

func benchPubSub(a, b *node.Node, count, size int) (benchResult, error) {
	ctx := context.Background()
	topic := "bench." + b.ID()
	var got atomic.Int64
	done := make(chan struct{})
	var once sync.Once
	var firstAt, lastAt atomic.Int64
	sub, err := b.Subscribe(ctx, topic, func(m *node.Message) {
		now := time.Now().UnixNano()
		firstAt.CompareAndSwap(0, now)
		lastAt.Store(now)
		if got.Add(1) == int64(count) {
			once.Do(func() { close(done) })
		}
	})
	if err != nil {
		return benchResult{}, err
	}
	defer sub.Unsubscribe()
	if _, err := b.Ping(ctx); err != nil { // subscription is active at the relay
		return benchResult{}, err
	}
	payload := make([]byte, size)
	rand.Read(payload)
	start := time.Now()
	for i := 0; i < count; i++ {
		if err := a.Publish(ctx, topic, payload); err != nil {
			return benchResult{}, err
		}
	}
	sendDur := time.Since(start)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
	}
	total := time.Since(start)
	if lastAt.Load() > 0 {
		total = time.Duration(lastAt.Load() - start.UnixNano())
	}
	n := got.Load()
	return benchResult{Name: "msg/pubsub", Values: map[string]float64{
		"msgs_per_sec":  float64(n) / total.Seconds(),
		"MB_per_sec":    float64(n) * float64(size) / total.Seconds() / 1e6,
		"delivered_pct": float64(n) * 100 / float64(count),
		"send_per_sec":  float64(count) / sendDur.Seconds(),
	}}, nil
}

func benchRequests(a, b *node.Node, count, size, conc int) ([]benchResult, error) {
	ctx := context.Background()
	b.OnRequest(func(m *node.Message) ([]byte, error) { return m.Data, nil })
	defer b.OnRequest(nil)
	payload := make([]byte, size)
	rand.Read(payload)
	for i := 0; i < 50; i++ { // warm up
		if _, err := a.Request(ctx, b.ID(), payload); err != nil {
			return nil, err
		}
	}
	lat := make([]time.Duration, 0, count)
	for i := 0; i < count; i++ {
		t0 := hrtime.Now()
		if _, err := a.Request(ctx, b.ID(), payload); err != nil {
			return nil, err
		}
		lat = append(lat, hrtime.Since(t0))
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(p float64) float64 {
		return float64(lat[int(float64(len(lat)-1)*p)]) / float64(time.Microsecond)
	}
	r1 := benchResult{Name: "req/latency_us", Values: map[string]float64{"p50": pct(0.5), "p90": pct(0.9), "p99": pct(0.99), "max": pct(1)},
		Notes: "round trip a -> relay -> b -> relay -> a"}

	var wg sync.WaitGroup
	var okCount atomic.Int64
	per := count / conc
	if per < 1 {
		per = 1
	}
	start := time.Now()
	for c := 0; c < conc; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				if _, err := a.Request(ctx, b.ID(), payload); err == nil {
					okCount.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	r2 := benchResult{Name: "req/throughput", Values: map[string]float64{"req_per_sec": float64(okCount.Load()) / time.Since(start).Seconds(), "concurrency": float64(conc)}}
	return []benchResult{r1, r2}, nil
}

func benchPipe(a, b *node.Node, route node.Route, size int64) (benchResult, error) {
	b.HandlePipe("bench.sink", func(p *node.Pipe) {
		n, _ := io.Copy(io.Discard, p)
		var buf [8]byte
		for i := 0; i < 8; i++ {
			buf[i] = byte(n >> (8 * i))
		}
		p.Write(buf[:])
		p.CloseWrite()
		p.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	p, err := a.OpenPipe(ctx, b.ID(), "bench.sink", node.WithRoute(route))
	if err != nil {
		return benchResult{}, err
	}
	defer p.Close()
	buf := make([]byte, 1<<20)
	rand.Read(buf)
	start := time.Now()
	var sent int64
	for sent < size {
		chunk := buf
		if rem := size - sent; rem < int64(len(chunk)) {
			chunk = chunk[:rem]
		}
		n, err := p.Write(chunk)
		sent += int64(n)
		if err != nil {
			return benchResult{}, err
		}
	}
	p.CloseWrite()
	var ack [8]byte
	if _, err := io.ReadFull(p, ack[:]); err != nil {
		return benchResult{}, err
	}
	dur := time.Since(start)
	var recv int64
	for i := 0; i < 8; i++ {
		recv |= int64(ack[i]) << (8 * i)
	}
	if recv != size {
		return benchResult{}, fmt.Errorf("receiver got %d of %d bytes", recv, size)
	}
	path := "relay"
	if p.Direct {
		path = "direct"
	}
	return benchResult{Name: "pipe/" + string(route), Values: map[string]float64{"MB_per_sec": float64(size) / dur.Seconds() / 1e6, "seconds": dur.Seconds()}, Notes: "path=" + path}, nil
}

func benchFile(a, b *node.Node, route node.Route, size int64) (benchResult, error) {
	dir, err := os.MkdirTemp("", "magnetk-bench-")
	if err != nil {
		return benchResult{}, err
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "src.bin")
	f, err := os.Create(src)
	if err != nil {
		return benchResult{}, err
	}
	buf := make([]byte, 4<<20)
	for written := int64(0); written < size; written += int64(len(buf)) {
		rand.Read(buf)
		n := int64(len(buf))
		if size-written < n {
			n = size - written
		}
		f.Write(buf[:n])
	}
	f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	t0 := time.Now()
	sf, err := b.Share(ctx, src, nil)
	if err != nil {
		return benchResult{}, err
	}
	hashDur := time.Since(t0)
	defer b.Unshare(ctx, sf.Hash)
	out := filepath.Join(dir, "out-"+string(route)+".bin")
	res, err := a.Download(ctx, sf.Link, out, &node.DownloadOptions{Route: route, NoResume: true})
	if err != nil {
		return benchResult{}, err
	}
	return benchResult{Name: "file/" + string(route), Values: map[string]float64{
		"MB_per_sec":    float64(res.Bytes) / res.Elapsed.Seconds() / 1e6,
		"seconds":       res.Elapsed.Seconds(),
		"hash_MB_per_s": float64(size) / hashDur.Seconds() / 1e6,
	}, Notes: "verified"}, nil
}
