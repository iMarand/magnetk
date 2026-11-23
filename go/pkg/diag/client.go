package diag

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iMarand/magnetk/go/pkg/files"
	"github.com/iMarand/magnetk/go/pkg/hrtime"
	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/node"
)

// ClientConfig configures the test client.
type ClientConfig struct {
	ID        string   // node id (default "client-<random>")
	Relays    []string // relay(s) to use
	HostID    string   // diagnostics host node id (default "diag-host")
	Token     string
	Transport netx.Mode
	Device    string // free text describing the device (model, OS)
	DataDir   string // log file and downloads ("" = no log file, temp dir for downloads)

	PipeMB    int // bytes per pipe test (default 16 MiB)
	Messages  int // bulk message count (default 20000)
	Heartbeat time.Duration

	// OnLog receives every log line (e.g. to show it in a UI). Must not block.
	OnLog func(LogLine)
	// Logger receives engine logs (default: into the client log at DEBUG).
	Logger *log.Logger
}

// AllTests is the default test order.
var AllTests = []string{
	"connect", "ping", "rpc", "messages", "pubsub", "datagram",
	"upload.relay", "download.relay", "echo.relay",
	"upload.auto", "download.auto", "file", "auth",
}

// Client runs tests against a Host and streams logs to it.
type Client struct {
	cfg  ClientConfig
	node *node.Node
	ctx  context.Context
	stop context.CancelFunc

	block []byte

	seq     atomic.Uint64
	logMu   sync.Mutex
	pending []LogLine
	logFile *os.File
	logKick chan struct{}

	dgram chan *node.Message

	runMu      sync.Mutex
	runCancel  context.CancelFunc
	running    atomic.Bool
	monCancel  context.CancelFunc
	network    atomic.Value // string
	downSince  atomic.Int64 // unix nanos, 0 = up
	lastObs    atomic.Value // string
	lastProto  atomic.Value // string
	reconnects atomic.Int64
	wg         sync.WaitGroup
}

// NewClient creates a client; Start connects it.
func NewClient(cfg ClientConfig) (*Client, error) {
	if len(cfg.Relays) == 0 {
		return nil, errors.New("diag: no relay given")
	}
	if cfg.HostID == "" {
		cfg.HostID = DefaultHostID
	}
	if cfg.ID == "" {
		b := make([]byte, 3)
		rand.Read(b)
		cfg.ID = "client-" + hex.EncodeToString(b)
	}
	if cfg.PipeMB <= 0 {
		cfg.PipeMB = 16
	}
	if cfg.Messages <= 0 {
		cfg.Messages = 20000
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = 2 * time.Second
	}
	c := &Client{cfg: cfg, logKick: make(chan struct{}, 1), dgram: make(chan *node.Message, 4096)}
	c.network.Store("unknown")
	c.lastObs.Store("")
	c.lastProto.Store("")
	c.block = make([]byte, 1<<20)
	rand.Read(c.block)
	c.ctx, c.stop = context.WithCancel(context.Background())
	if cfg.DataDir != "" {
		os.MkdirAll(cfg.DataDir, 0o755)
		c.logFile, _ = os.OpenFile(filepath.Join(cfg.DataDir, "magnetk-diag.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	}
	return c, nil
}

// ID returns the client node id.
func (c *Client) ID() string { return c.cfg.ID }

// Node returns the underlying node (nil before Start).
func (c *Client) Node() *node.Node { return c.node }

// Start creates the node and connects in the background; it returns once
// the first connection succeeded or ctx ended (the node keeps retrying).
func (c *Client) Start(ctx context.Context) error {
	logger := c.cfg.Logger
	if logger == nil {
		logger = log.New(logWriter{c}, "", 0)
	}
	n, err := node.New(node.Config{
		ID: c.cfg.ID, Token: c.cfg.Token, Relays: c.cfg.Relays, Transport: c.cfg.Transport,
		Meta: c.cfg.Device, CacheDir: "-", Logger: logger,
		HeartbeatInterval: c.cfg.Heartbeat, HeartbeatTimeout: 3 * c.cfg.Heartbeat,
		ReconnectMin: 200 * time.Millisecond, ReconnectMax: 3 * time.Second,
		OnEvent: c.onEvent,
	})
	if err != nil {
		c.logf("ERROR", "cannot create node: %v", err)
		return err
	}
	c.node = n
	c.logf("INFO", "magnetk diag client %s (%s/%s, %s) relays=%v transport=%s device=%q",
		c.cfg.ID, runtime.GOOS, runtime.GOARCH, runtime.Version(), c.cfg.Relays, orDefault(string(c.cfg.Transport), "auto"), c.cfg.Device)

	c.wg.Add(2)
	go c.flushLogs()
	go c.inbox()

	t0 := time.Now()
	if err := n.Start(ctx); err != nil {
		c.logf("ERROR", "connect failed after %v: %v (still retrying in background)", time.Since(t0).Round(time.Millisecond), err)
		return err
	}
	c.logf("INFO", "connected in %v", time.Since(t0).Round(time.Millisecond))
	c.subscribeCommands()
	return nil
}

func (c *Client) subscribeCommands() {
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	for _, t := range []string{TopicCmd + "." + c.cfg.ID, TopicCmd + ".all"} {
		if _, err := c.node.Subscribe(ctx, t, c.onCommand); err != nil {
			c.logf("WARN", "subscribe %s: %v", t, err)
		}
	}
}

func (c *Client) onEvent(ev node.Event) {
	switch ev.Type {
	case node.EventConnected:
		down := c.downSince.Swap(0)
		obs := ""
		if c.node != nil {
			obs = c.node.Info().Observed
		}
		prevObs, _ := c.lastObs.Swap(obs).(string)
		prevProto, _ := c.lastProto.Swap(ev.Protocol).(string)
		msg := fmt.Sprintf("connected to %s (relay %s) via %s, public address %s, network %s", ev.Relay, ev.RelayID, ev.Protocol, obs, c.network.Load())
		if down != 0 {
			d := time.Since(time.Unix(0, down))
			msg += fmt.Sprintf(" — back after %v", d.Round(time.Millisecond))
			c.publishResult(Result{Test: "reconnect", OK: true, Ms: float64(d.Microseconds()) / 1000, Metrics: map[string]any{
				"downtime_ms": round2(float64(d.Microseconds()) / 1000), "relay": ev.Relay, "protocol": ev.Protocol,
				"public_addr": obs, "prev_public_addr": prevObs, "prev_protocol": prevProto, "address_changed": prevObs != "" && prevObs != obs,
			}})
		}
		c.logf("INFO", "%s", msg)
	case node.EventDisconnected:
		c.downSince.CompareAndSwap(0, time.Now().UnixNano())
		c.reconnects.Add(1)
		c.logf("WARN", "disconnected from %s: %v", ev.Relay, ev.Err)
	case node.EventError:
		c.logf("ERROR", "node error: %v", ev.Err)
	}
}

// NetworkChanged tells the client the device switched networks (desc is
// e.g. "wifi", "cellular", "none"); the node reconnects at once.
func (c *Client) NetworkChanged(desc string) {
	prev, _ := c.network.Swap(desc).(string)
	if prev == desc {
		return
	}
	c.logf("INFO", "network changed: %s -> %s", prev, desc)
	if c.node != nil && prev != "unknown" && desc != "none" {
		c.downSince.CompareAndSwap(0, time.Now().UnixNano())
		c.node.Reconnect()
	}
}

// Close stops tests and disconnects (pending logs are flushed first).
func (c *Client) Close() {
	c.StopTests()
	c.Monitor(false)
	if c.node != nil {
		c.logf("INFO", "closing")
		c.flushOnce(2 * time.Second)
	}
	c.stop()
	if c.node != nil {
		c.node.Close()
	}
	c.wg.Wait()
	if c.logFile != nil {
		c.logFile.Close()
	}
}

// ===== logging =====

type logWriter struct{ c *Client }

func (w logWriter) Write(p []byte) (int, error) {
	w.c.logf("DEBUG", "%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// Log adds a line to the client log (sent to the host like every other line).
func (c *Client) Log(level, msg string) { c.logf(level, "%s", msg) }

func (c *Client) logf(level, format string, args ...any) {
	l := LogLine{T: time.Now().UnixMilli(), Level: level, Msg: fmt.Sprintf(format, args...), Seq: c.seq.Add(1)}
	if c.cfg.OnLog != nil {
		c.cfg.OnLog(l)
	}
	c.logMu.Lock()
	if c.logFile != nil {
		fmt.Fprintf(c.logFile, "%s %-5s %s\n", time.UnixMilli(l.T).Format("2006-01-02 15:04:05.000"), l.Level, l.Msg)
	}
	c.pending = append(c.pending, l)
	if len(c.pending) > 20000 { // keep the newest while offline
		c.pending = c.pending[len(c.pending)-20000:]
	}
	c.logMu.Unlock()
	select {
	case c.logKick <- struct{}{}:
	default:
	}
}

// flushLogs publishes buffered log lines in batches; while disconnected
// they are kept and sent after reconnecting, so the host sees everything.
func (c *Client) flushLogs() {
	defer c.wg.Done()
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		case <-c.logKick:
			time.Sleep(50 * time.Millisecond) // batch bursts
		}
		c.flushOnce(time.Second)
	}
}

func (c *Client) flushOnce(timeout time.Duration) {
	if c.node == nil || !c.node.Connected() {
		return
	}
	for {
		c.logMu.Lock()
		batch := c.pending
		if len(batch) > 200 {
			batch = batch[:200]
		}
		c.logMu.Unlock()
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := c.node.Publish(ctx, TopicLog+"."+c.cfg.ID, mustJSON(batch))
		cancel()
		if err != nil {
			return
		}
		c.logMu.Lock()
		c.pending = c.pending[len(batch):]
		c.logMu.Unlock()
	}
}

func (c *Client) publishResult(r Result) {
	r.Client = c.cfg.ID
	r.Network, _ = c.network.Load().(string)
	if c.node != nil {
		r.Protocol = c.node.Info().Protocol
	}
	if r.Time == 0 {
		r.Time = time.Now().UnixMilli()
	}
	status := "PASS"
	if !r.OK {
		status = "FAIL"
	}
	m, _ := json.Marshal(r.Metrics)
	line := fmt.Sprintf("%s %s %.0fms %s", status, r.Test, r.Ms, m)
	if r.Error != "" {
		line += " error=" + r.Error
	}
	c.logf("RESULT", "%s", line)
	payload := mustJSON(r)
	if c.node.Connected() {
		ctx, cancel := context.WithTimeout(c.ctx, 2*time.Second)
		err := c.node.Publish(ctx, TopicResult+"."+c.cfg.ID, payload)
		cancel()
		if err == nil {
			return
		}
	}
	go func() {
		// Retried until delivered (e.g. results produced while offline).
		for i := 0; i < 60 && c.ctx.Err() == nil; i++ {
			ctx, cancel := context.WithTimeout(c.ctx, 2*time.Second)
			err := c.node.Publish(ctx, TopicResult+"."+c.cfg.ID, payload)
			cancel()
			if err == nil {
				return
			}
			time.Sleep(time.Second)
		}
	}()
}

// ===== commands =====

func (c *Client) onCommand(m *node.Message) {
	var cmd Command
	if err := json.Unmarshal(m.Data, &cmd); err != nil {
		c.logf("WARN", "bad command: %v", err)
		return
	}
	c.logf("INFO", "command from %s: %s", m.From, m.Data)
	switch cmd.Op {
	case "run":
		if cmd.PipeMB > 0 {
			c.cfg.PipeMB = cmd.PipeMB
		}
		c.RunAsync(cmd.Tests, cmd.Repeat)
	case "stop":
		c.StopTests()
	case "monitor":
		c.Monitor(cmd.On)
	case "reconnect":
		c.node.Reconnect()
	case "info":
		c.logStatus()
	}
}

func (c *Client) logStatus() {
	in := c.node.Info()
	sec := c.node.Security()
	st := c.node.Stats()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	c.logf("INFO", "status: connected=%v relay=%s proto=%s public=%s rtt=%v tls=%s %s network=%s reconnects=%d sent=%d recv=%d heap=%dMB goroutines=%d",
		in.Connected, in.Relay, in.Protocol, in.Observed, in.RTT.Round(time.Microsecond), sec.Version, sec.Cipher, c.network.Load(), c.reconnects.Load(), st.Sent, st.Received, ms.HeapAlloc>>20, runtime.NumGoroutine())
}

// Status returns a one-line connection summary (for UIs).
func (c *Client) Status() string {
	if c.node == nil {
		return "not started"
	}
	in := c.node.Info()
	if !in.Connected {
		return fmt.Sprintf("connecting… (network %s, %d reconnects)", c.network.Load(), c.reconnects.Load())
	}
	state := "idle"
	if c.running.Load() {
		state = "testing"
	}
	if c.monCancel != nil {
		state += "+monitor"
	}
	return fmt.Sprintf("%s via %s · %s · rtt %v · %s · %s", in.Relay, in.Protocol, c.network.Load(), in.RTT.Round(100*time.Microsecond), in.Observed, state)
}

// RunAsync runs tests in the background (one run at a time).
func (c *Client) RunAsync(tests []string, repeat int) {
	go c.Run(tests, repeat)
}

// StopTests cancels the running tests.
func (c *Client) StopTests() {
	c.runMu.Lock()
	if c.runCancel != nil {
		c.runCancel()
	}
	c.runMu.Unlock()
}

// Run runs the given tests ("all" or empty = AllTests) repeat times.
func (c *Client) Run(tests []string, repeat int) []Result {
	if !c.running.CompareAndSwap(false, true) {
		c.logf("WARN", "tests already running")
		return nil
	}
	defer c.running.Store(false)
	if len(tests) == 0 || len(tests) == 1 && tests[0] == "all" {
		tests = AllTests
	}
	if repeat <= 0 {
		repeat = 1
	}
	ctx, cancel := context.WithCancel(c.ctx)
	c.runMu.Lock()
	c.runCancel = cancel
	c.runMu.Unlock()
	defer cancel()

	var out []Result
	pass := 0
	t0 := time.Now()
	for r := 0; r < repeat && ctx.Err() == nil; r++ {
		if repeat > 1 {
			c.logf("INFO", "=== round %d/%d ===", r+1, repeat)
		}
		for _, name := range tests {
			if ctx.Err() != nil {
				break
			}
			res := c.runOne(ctx, name)
			out = append(out, res)
			if res.OK {
				pass++
			}
		}
	}
	c.logf("INFO", "=== %d/%d tests passed in %v ===", pass, len(out), time.Since(t0).Round(time.Millisecond))
	return out
}

func (c *Client) runOne(ctx context.Context, name string) (res Result) {
	c.logf("INFO", "--- %s ---", name)
	t0 := time.Now()
	metrics := map[string]any{}
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v", p)
			}
		}()
		tctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		if c.node == nil {
			err = errors.New("client not started")
			return
		}
		if !c.node.Connected() {
			wctx, wcancel := context.WithTimeout(tctx, 20*time.Second)
			err = c.node.WaitConnected(wctx)
			wcancel()
			if err != nil {
				return
			}
		}
		err = c.test(tctx, name, metrics)
	}()
	res = Result{Test: name, OK: err == nil, Ms: float64(time.Since(t0).Microseconds()) / 1000, Metrics: metrics}
	if err != nil {
		res.Error = err.Error()
	}
	c.publishResult(res)
	return res
}

func (c *Client) test(ctx context.Context, name string, m map[string]any) error {
	base, route, _ := strings.Cut(name, ".")
	r := node.Route(route)
	switch base {
	case "connect":
		return c.testConnect(ctx, m)
	case "ping":
		return c.testPing(ctx, m)
	case "rpc":
		return c.testRPC(ctx, m)
	case "messages":
		return c.testMessages(ctx, m)
	case "pubsub":
		return c.testPubSub(ctx, m)
	case "datagram":
		return c.testDatagram(ctx, m)
	case "upload":
		return c.testUpload(ctx, orRoute(r), m)
	case "download":
		return c.testDownload(ctx, orRoute(r), m)
	case "echo":
		return c.testEcho(ctx, orRoute(r), m)
	case "file":
		return c.testFile(ctx, m)
	case "auth":
		return c.testAuth(ctx, m)
	}
	return fmt.Errorf("unknown test %q", name)
}

func orRoute(r node.Route) node.Route {
	if r == "" {
		return node.RouteAuto
	}
	return r
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (c *Client) ctl(ctx context.Context, req ctlReq, out any) error {
	b, err := c.node.RequestTopic(ctx, ctlTopic(c.cfg.HostID), mustJSON(req))
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// ===== tests =====

func (c *Client) testConnect(ctx context.Context, m map[string]any) error {
	in := c.node.Info()
	sec := c.node.Security()
	m["relay"], m["relay_id"], m["protocol"], m["public_addr"] = in.Relay, in.RelayID, in.Protocol, in.Observed
	m["tls"], m["cipher"], m["network"] = sec.Version, sec.Cipher, c.network.Load()
	m["transport_rtt_ms"] = round2(float64(in.RTT.Microseconds()) / 1000)
	t0, h0 := time.Now(), hrtime.Now()
	var h hostHello
	if err := c.ctl(ctx, ctlReq{Op: "hello", Info: c.cfg.Device + " / " + runtime.GOOS + "-" + runtime.GOARCH}, &h); err != nil {
		return fmt.Errorf("host %s not reachable: %w", c.cfg.HostID, err)
	}
	rtt := hrtime.Since(h0)
	t1 := t0.Add(rtt)
	m["host"] = h.Host
	m["host_version"] = h.Version
	m["hello_rtt_ms"] = round2(float64(rtt.Microseconds()) / 1000)
	mid := t0.Add(t1.Sub(t0) / 2)
	m["clock_offset_ms"] = round2(float64(h.TimeNs-mid.UnixNano()) / 1e6)
	if !strings.HasPrefix(sec.Version, "TLS 1.3") {
		return fmt.Errorf("connection is not TLS 1.3 (%q)", sec.Version)
	}
	switch {
	case h.RelayCert == "" || in.Protocol != "tcp":
		m["cert_check"] = "n/a"
	case h.RelayCert == sec.PeerCert:
		m["cert_check"] = "match"
	default:
		m["cert_check"] = "MISMATCH"
		return fmt.Errorf("relay certificate %s.. differs from the host's %s.. (proxy or MITM?)", short(sec.PeerCert), short(h.RelayCert))
	}
	return nil
}

func short(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}

func (c *Client) testPing(ctx context.Context, m map[string]any) error {
	var samples []time.Duration
	lost := 0
	for i := 0; i < 40 && ctx.Err() == nil; i++ {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		d, err := c.node.Ping(pctx)
		cancel()
		if err != nil {
			lost++
		} else {
			samples = append(samples, d)
		}
		time.Sleep(25 * time.Millisecond)
	}
	m["relay_rtt"] = summarize(samples)
	m["lost"] = lost
	if len(samples) == 0 {
		return errors.New("no ping answered")
	}
	return nil
}

func (c *Client) testRPC(ctx context.Context, m map[string]any) error {
	topic := echoTopic(c.cfg.HostID)
	var small []time.Duration
	payload := make([]byte, 64)
	for i := 0; i < 100 && ctx.Err() == nil; i++ {
		rand.Read(payload)
		t0 := hrtime.Now()
		r, err := c.node.RequestTopic(ctx, topic, payload)
		if err != nil {
			return fmt.Errorf("request %d: %w", i, err)
		}
		small = append(small, hrtime.Since(t0))
		if !bytes.Equal(r, payload) {
			return fmt.Errorf("request %d: reply differs from request (corruption)", i)
		}
	}
	m["rtt_64B"] = summarize(small)
	var big []time.Duration
	var total int64
	t0 := time.Now()
	for i := 0; i < 10 && ctx.Err() == nil; i++ {
		p := c.block[i*1000 : i*1000+256<<10]
		t1 := hrtime.Now()
		r, err := c.node.RequestTopic(ctx, topic, p)
		if err != nil {
			return fmt.Errorf("256KB request %d: %w", i, err)
		}
		big = append(big, hrtime.Since(t1))
		if !bytes.Equal(r, p) {
			return fmt.Errorf("256KB request %d: reply corrupted", i)
		}
		total += 2 * int64(len(p))
	}
	m["rtt_256KB"] = summarize(big)
	m["roundtrip_MBps"] = mbps(total, time.Since(t0))
	// concurrent requests: how latency behaves under load
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conc []time.Duration
	var cerr error
	t2 := time.Now()
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				t := hrtime.Now()
				if _, err := c.node.RequestTopic(ctx, topic, payload); err != nil {
					mu.Lock()
					cerr = err
					mu.Unlock()
					return
				}
				mu.Lock()
				conc = append(conc, hrtime.Since(t))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	m["rtt_16_concurrent"] = summarize(conc)
	m["requests_per_s"] = round2(float64(len(conc)) / time.Since(t2).Seconds())
	return cerr
}

func (c *Client) testMessages(ctx context.Context, m map[string]any) error {
	n := c.cfg.Messages
	if err := c.ctl(ctx, ctlReq{Op: "bulk_start"}, nil); err != nil {
		return err
	}
	msg := make([]byte, 256)
	t0 := time.Now()
	for i := 0; i < n; i++ {
		msg[0] = 'B'
		s := strconv.Itoa(i)
		copy(msg[1:], s)
		for j := 1 + len(s); j < 12; j++ {
			msg[j] = ' '
		}
		if err := c.node.Send(ctx, c.cfg.HostID, msg); err != nil {
			return fmt.Errorf("send %d: %w", i, err)
		}
	}
	sendDur := time.Since(t0)
	var br bulkResult
	if err := c.ctl(ctx, ctlReq{Op: "bulk_result", N: n}, &br); err != nil {
		return err
	}
	total := time.Since(t0)
	m["sent"], m["received"], m["out_of_order"] = n, br.Count, br.OutOfOrder
	m["loss_pct"] = round2(float64(n-br.Count) * 100 / float64(n))
	m["send_msgs_per_s"] = round2(float64(n) / sendDur.Seconds())
	m["delivered_msgs_per_s"] = round2(float64(br.Count) / total.Seconds())
	m["MBps"] = mbps(br.Bytes, total)
	if br.Count < n {
		return fmt.Errorf("%d of %d messages lost", n-br.Count, n)
	}
	if br.OutOfOrder > 0 {
		return fmt.Errorf("%d messages out of order", br.OutOfOrder)
	}
	return nil
}

func (c *Client) testPubSub(ctx context.Context, m map[string]any) error {
	const n = 5000
	topic := "diag.fan." + c.cfg.ID
	var got atomic.Int64
	var first, last atomic.Int64
	done := make(chan struct{})
	sub, err := c.node.Subscribe(ctx, topic, func(msg *node.Message) {
		now := time.Now().UnixNano()
		first.CompareAndSwap(0, now)
		last.Store(now)
		if got.Add(1) == n {
			close(done)
		}
	})
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()
	t0 := time.Now()
	if err := c.ctl(ctx, ctlReq{Op: "fanout", N: n, Size: 256, Topic: topic}, nil); err != nil {
		return err
	}
	idle := time.NewTicker(500 * time.Millisecond)
	defer idle.Stop()
	prev := int64(-1)
wait:
	for {
		select {
		case <-done:
			break wait
		case <-ctx.Done():
			break wait
		case <-idle.C:
			g := got.Load()
			if g == prev && time.Since(t0) > 3*time.Second {
				break wait
			}
			prev = g
		}
	}
	g := got.Load()
	m["published"], m["received"] = n, g
	if f, l := first.Load(), last.Load(); f != 0 && l > f {
		m["msgs_per_s"] = round2(float64(g) / time.Duration(l-f).Seconds())
	}
	m["first_msg_ms"] = round2(float64(time.Duration(first.Load()-t0.UnixNano()).Microseconds()) / 1000)
	if sub.Dropped() > 0 {
		m["dropped_locally"] = sub.Dropped()
	}
	if g < n {
		return fmt.Errorf("received %d of %d published messages", g, n)
	}
	return nil
}

func (c *Client) inbox() {
	defer c.wg.Done()
	for {
		select {
		case <-c.ctx.Done():
			return
		case msg, ok := <-c.node.Messages():
			if !ok {
				return
			}
			if msg.Kind == node.KindDirect && len(msg.Data) > 0 && msg.Data[0] == 'D' {
				select {
				case c.dgram <- msg:
				default:
				}
			}
		}
	}
}

func (c *Client) testDatagram(ctx context.Context, m map[string]any) error {
	const n = 300
	for len(c.dgram) > 0 { // drop stale echoes
		<-c.dgram
	}
	sent := map[int]hrtime.Time{}
	var mu sync.Mutex
	var rtts []time.Duration
	viaDatagram := 0
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		timeout := time.NewTimer(time.Hour)
		defer timeout.Stop()
		for {
			select {
			case msg := <-c.dgram:
				seq, err := strconv.Atoi(strings.TrimSpace(string(msg.Data[1:min(len(msg.Data), 12)])))
				if err != nil {
					continue
				}
				mu.Lock()
				if t, ok := sent[seq]; ok {
					rtts = append(rtts, hrtime.Since(t))
					delete(sent, seq)
					if msg.Datagram {
						viaDatagram++
					}
				}
				mu.Unlock()
			case <-ctx.Done():
				return
			case <-timeout.C:
				return
			}
			mu.Lock()
			if len(rtts) == n {
				mu.Unlock()
				return
			}
			mu.Unlock()
			timeout.Reset(2 * time.Second)
		}
	}()
	buf := make([]byte, 200)
	for i := 0; i < n && ctx.Err() == nil; i++ {
		buf[0] = 'D'
		s := strconv.Itoa(i)
		copy(buf[1:], s)
		for j := 1 + len(s); j < 12; j++ {
			buf[j] = ' '
		}
		mu.Lock()
		sent[i] = hrtime.Now()
		mu.Unlock()
		if err := c.node.Send(ctx, c.cfg.HostID, buf, node.Unreliable()); err != nil {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
	<-recvDone
	mu.Lock()
	defer mu.Unlock()
	m["sent"], m["echoed"], m["via_datagram"] = n, len(rtts), viaDatagram
	m["loss_pct"] = round2(float64(n-len(rtts)) * 100 / n)
	m["rtt"] = summarize(rtts)
	if len(rtts) == 0 {
		return errors.New("no datagram echo received")
	}
	return nil
}

func (c *Client) openPipe(ctx context.Context, service string, route node.Route, m map[string]any) (*node.Pipe, error) {
	t0 := hrtime.Now()
	p, err := c.node.OpenPipe(ctx, c.cfg.HostID, service, node.WithRoute(route))
	if err != nil {
		return nil, err
	}
	m["open_ms"] = round2(float64(hrtime.Since(t0).Microseconds()) / 1000)
	m["direct"] = p.Direct
	if dl, ok := ctx.Deadline(); ok {
		p.SetDeadline(dl)
	}
	return p, nil
}

func (c *Client) testUpload(ctx context.Context, route node.Route, m map[string]any) error {
	p, err := c.openPipe(ctx, ServiceSink, route, m)
	if err != nil {
		return err
	}
	defer p.Close()
	total := int64(c.cfg.PipeMB) << 20
	sum := sha256.New()
	t0 := time.Now()
	for left := total; left > 0; {
		chunk := c.block
		if int64(len(chunk)) > left {
			chunk = chunk[:left]
		}
		if _, err := p.Write(chunk); err != nil {
			return fmt.Errorf("write after %d bytes: %w", total-left, err)
		}
		sum.Write(chunk)
		left -= int64(len(chunk))
	}
	p.CloseWrite()
	line, err := io.ReadAll(io.LimitReader(p, 4096))
	if err != nil {
		return err
	}
	el := time.Since(t0)
	var sr sinkResult
	if err := json.Unmarshal(bytes.TrimSpace(line), &sr); err != nil {
		return fmt.Errorf("bad sink answer %q", line)
	}
	m["bytes"], m["MBps"], m["host_MBps"] = total, mbps(total, el), mbps(sr.Bytes, time.Duration(sr.Ms*1e6))
	if sr.Bytes != total {
		return fmt.Errorf("host received %d of %d bytes", sr.Bytes, total)
	}
	if sr.SHA256 != hex.EncodeToString(sum.Sum(nil)) {
		return errors.New("sha256 mismatch: data corrupted in transit")
	}
	m["verified"] = true
	return nil
}

func (c *Client) testDownload(ctx context.Context, route node.Route, m map[string]any) error {
	p, err := c.openPipe(ctx, ServiceSource, route, m)
	if err != nil {
		return err
	}
	defer p.Close()
	total := int64(c.cfg.PipeMB) << 20
	t0 := time.Now()
	if _, err := p.Write(append(mustJSON(sourceReq{Bytes: total}), '\n')); err != nil {
		return err
	}
	sum := sha256.New()
	var firstByte time.Duration
	buf := make([]byte, 256<<10)
	var got int64
	for got < total {
		k, err := p.Read(buf[:min(int64(len(buf)), total-got)])
		if k > 0 && got == 0 {
			firstByte = time.Since(t0)
		}
		sum.Write(buf[:k])
		got += int64(k)
		if err != nil {
			return fmt.Errorf("read after %d of %d bytes: %w", got, total, err)
		}
	}
	trailer := make([]byte, 32)
	if _, err := io.ReadFull(p, trailer); err != nil {
		return fmt.Errorf("missing checksum: %w", err)
	}
	el := time.Since(t0)
	m["bytes"], m["MBps"], m["first_byte_ms"] = total, mbps(total, el), round2(float64(firstByte.Microseconds())/1000)
	if !bytes.Equal(trailer, sum.Sum(nil)) {
		return errors.New("sha256 mismatch: data corrupted in transit")
	}
	m["verified"] = true
	return nil
}

func (c *Client) testEcho(ctx context.Context, route node.Route, m map[string]any) error {
	p, err := c.openPipe(ctx, ServiceEcho, route, m)
	if err != nil {
		return err
	}
	defer p.Close()
	total := int64(c.cfg.PipeMB) << 20 / 2
	sentSum := sha256.New()
	t0 := time.Now()
	werr := make(chan error, 1)
	go func() {
		for left := total; left > 0; {
			chunk := c.block
			if int64(len(chunk)) > left {
				chunk = chunk[:left]
			}
			if _, err := p.Write(chunk); err != nil {
				werr <- err
				return
			}
			sentSum.Write(chunk)
			left -= int64(len(chunk))
		}
		werr <- p.CloseWrite()
	}()
	recvSum := sha256.New()
	got, err := io.Copy(recvSum, p)
	if err != nil {
		return fmt.Errorf("read after %d bytes: %w", got, err)
	}
	if err := <-werr; err != nil {
		return err
	}
	el := time.Since(t0)
	m["bytes_each_way"], m["MBps_each_way"] = total, mbps(total, el)
	if got != total || !bytes.Equal(sentSum.Sum(nil), recvSum.Sum(nil)) {
		return fmt.Errorf("echo mismatch: sent %d bytes, got %d back (or different content)", total, got)
	}
	m["verified"] = true
	return nil
}

func (c *Client) testFile(ctx context.Context, m map[string]any) error {
	var h hostHello
	if err := c.ctl(ctx, ctlReq{Op: "hello", Info: c.cfg.Device}, &h); err != nil {
		return err
	}
	if h.FileLink == "" {
		return errors.New("host shares no test file")
	}
	dir := c.cfg.DataDir
	if dir == "" {
		dir = os.TempDir()
	}
	out := filepath.Join(dir, "diag-download.bin")
	os.Remove(out)
	defer os.Remove(out)
	var lastPct float64
	res, err := c.node.Download(ctx, h.FileLink, out, &node.DownloadOptions{NoResume: true, Progress: func(p files.Progress) {
		if pct := p.Percent(); pct-lastPct >= 25 {
			lastPct = pct
			c.logf("INFO", "file: %.0f%% %.1f MB/s sources=%d streams=%d", pct, p.BytesPerSec/1e6, p.Sources, p.Streams)
		}
	}})
	if err != nil {
		return err
	}
	m["bytes"], m["MBps"] = res.Manifest.Size, mbps(res.Manifest.Size, res.Elapsed)
	m["chunks"] = res.Manifest.NumChunks()
	m["verified"] = true // Download verifies every chunk and the whole-file hash
	return nil
}

func (c *Client) testAuth(ctx context.Context, m map[string]any) error {
	var h hostHello
	if err := c.ctl(ctx, ctlReq{Op: "hello", Info: c.cfg.Device}, &h); err != nil {
		return err
	}
	if !h.RelayToken {
		m["relay_requires_token"] = false
		c.logf("WARN", "auth: the relay accepts nodes without a token (start the host with -token to test authentication)")
		return nil
	}
	m["relay_requires_token"] = true
	bad, err := node.New(node.Config{Relays: []string{c.node.Info().Relay}, Token: "wrong-" + c.cfg.ID, Transport: c.cfg.Transport, CacheDir: "-"})
	if err != nil {
		return err
	}
	defer bad.Close()
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err = bad.Start(sctx)
	if errors.Is(err, node.ErrUnauthorized) {
		m["wrong_token_rejected"] = true
		return nil
	}
	return fmt.Errorf("relay accepted a wrong token (err=%v)", err)
}

// ===== monitor =====

// Monitor pings the relay every second and logs RTT and connection state
// changes; use it while switching networks.
func (c *Client) Monitor(on bool) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if c.monCancel != nil {
		c.monCancel()
		c.monCancel = nil
		c.logf("INFO", "monitor stopped")
	}
	if !on || c.node == nil {
		return
	}
	ctx, cancel := context.WithCancel(c.ctx)
	c.monCancel = cancel
	c.logf("INFO", "monitor started (1 ping/s)")
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		var window []time.Duration
		lost := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
			d, err := c.node.Ping(pctx)
			pcancel()
			in := c.node.Info()
			if err != nil {
				lost++
				c.logf("WARN", "mon: ping failed (%v) network=%s connected=%v", err, c.network.Load(), in.Connected)
			} else {
				window = append(window, d)
				c.logf("INFO", "mon: rtt=%.1fms proto=%s network=%s public=%s", float64(d.Microseconds())/1000, in.Protocol, c.network.Load(), in.Observed)
			}
			if len(window)+lost >= 30 {
				c.publishResult(Result{Test: "monitor", OK: lost == 0, Metrics: map[string]any{"rtt": summarize(window), "lost": lost, "network": c.network.Load()}})
				window, lost = window[:0], 0
			}
		}
	}()
}
