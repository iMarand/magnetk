package diag

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/node"
	"github.com/iMarand/magnetk/go/pkg/relay"
)

// HostConfig configures a diagnostics host.
type HostConfig struct {
	ID string // node id (default "diag-host")

	// RelayListen runs an embedded relay on this address (e.g. "0.0.0.0:4002").
	// Leave empty to use Relays instead.
	RelayListen string
	// Relays the host node connects to (e.g. a VPS relay) when RelayListen
	// is empty; with an embedded relay they are federated with it instead.
	Relays        []string
	ClusterSecret string

	Token     string    // relay token (embedded relay requires it; node presents it)
	Transport netx.Mode // host node transport (default auto)

	// DirectListen is the host node's peer-to-peer address (default
	// "0.0.0.0:4012"; "-" disables direct connections).
	DirectListen string
	Advertise    []string // extra public "host:port" to advertise for direct pipes

	FileSize int64  // size of the shared test file (default 32 MiB, 0 = default, <0 = none)
	DataDir  string // logs and test file (default "./diag-data")
	HTTP     string // HTTP API address (default "127.0.0.1:8090", "-" = off)

	Logger *log.Logger // engine logs (default: discarded)
	Out    io.Writer   // human readable client logs (default stdout)
}

// Host serves the test services and collects client logs.
type Host struct {
	cfg   HostConfig
	relay *relay.Server
	node  *node.Node
	http  *http.Server

	block    []byte // random data served by the source service
	fileLink string
	fileSize int64
	relayFP  string

	mu      sync.Mutex
	bulk    map[string]*bulkState
	clients map[string]*clientState
	out     io.Writer
	outMu   sync.Mutex
}

type bulkState struct {
	count, ooo  int
	bytes       int64
	lastSeq     int
	first, last time.Time
	started     time.Time
}

type clientState struct {
	ID        string    `json:"id"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Online    bool      `json:"online"`
	Lines     int       `json:"lines"`
	Results   int       `json:"results"`
	Failed    int       `json:"failed"`
	Info      string    `json:"info,omitempty"`
	file      *os.File
	logs      []string // recent formatted lines
	results   []Result
}

// StartHost starts a diagnostics host.
func StartHost(ctx context.Context, cfg HostConfig) (*Host, error) {
	if cfg.ID == "" {
		cfg.ID = DefaultHostID
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "diag-data"
	}
	if cfg.HTTP == "" {
		cfg.HTTP = "127.0.0.1:8090"
	}
	if cfg.DirectListen == "" {
		cfg.DirectListen = "0.0.0.0:4012"
	}
	if cfg.FileSize == 0 {
		cfg.FileSize = 32 << 20
	}
	if cfg.Out == nil {
		cfg.Out = os.Stdout
	}
	if cfg.RelayListen == "" && len(cfg.Relays) == 0 {
		return nil, errors.New("diag: set RelayListen (embedded relay) or Relays")
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "logs"), 0o755); err != nil {
		return nil, err
	}
	h := &Host{cfg: cfg, bulk: map[string]*bulkState{}, clients: map[string]*clientState{}, out: cfg.Out}
	h.block = make([]byte, 1<<20)
	rand.Read(h.block)

	relays := cfg.Relays
	if cfg.RelayListen != "" {
		rc := relay.Config{Listen: cfg.RelayListen, LegacyListen: "", Logger: cfg.Logger, Peers: cfg.Relays, ClusterSecret: cfg.ClusterSecret}
		if cfg.Token != "" {
			rc.AuthTokens = []string{cfg.Token}
		}
		r, err := relay.New(rc)
		if err != nil {
			return nil, err
		}
		if err := r.Start(); err != nil {
			r.Close()
			return nil, err
		}
		h.relay = r
		relays = []string{fmt.Sprintf("127.0.0.1:%d", r.Port())}
		h.relayFP = probeRelayCert(ctx, relays[0], cfg.Token)
	}

	ncfg := node.Config{
		ID: cfg.ID, Token: cfg.Token, Relays: relays, Transport: cfg.Transport,
		WatchPresence: true, InboxSize: 1 << 16, SubBuffer: 1 << 14, CacheDir: "-",
		Logger: cfg.Logger, AdvertiseAddrs: cfg.Advertise,
	}
	if cfg.DirectListen != "-" {
		ncfg.Listen = true
		ncfg.ListenAddr = cfg.DirectListen
	}
	n, err := node.New(ncfg)
	if err == nil {
		err = n.Start(ctx)
	}
	if err != nil {
		if n != nil {
			n.Close()
		}
		if h.relay != nil {
			h.relay.Close()
		}
		return nil, err
	}
	h.node = n

	if err := h.setup(ctx); err != nil {
		h.Close()
		return nil, err
	}
	if cfg.HTTP != "-" {
		if err := h.startHTTP(); err != nil {
			h.Close()
			return nil, err
		}
	}
	return h, nil
}

// probeRelayCert connects once over TCP to learn the relay's certificate
// fingerprint, which clients compare with what they see (MITM check).
func probeRelayCert(ctx context.Context, addr, token string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	n, err := node.New(node.Config{Relays: []string{addr}, Token: token, Transport: netx.ModeTCP, CacheDir: "-"})
	if err != nil {
		return ""
	}
	defer n.Close()
	if n.Start(ctx) != nil {
		return ""
	}
	return n.Security().PeerCert
}

func (h *Host) setup(ctx context.Context) error {
	n := h.node
	if _, err := n.HandleRequests(ctx, echoTopic(h.cfg.ID), func(m *node.Message) ([]byte, error) { return m.Data, nil }); err != nil {
		return err
	}
	if _, err := n.HandleRequests(ctx, ctlTopic(h.cfg.ID), h.handleCtl); err != nil {
		return err
	}
	n.HandlePipe(ServiceSink, h.serveSink)
	n.HandlePipe(ServiceSource, h.serveSource)
	n.HandlePipe(ServiceEcho, func(p *node.Pipe) {
		defer p.Close()
		io.Copy(p, p)
		p.CloseWrite()
	})
	if _, err := n.Subscribe(ctx, TopicLog+".>", h.onLog); err != nil {
		return err
	}
	if _, err := n.Subscribe(ctx, TopicResult+".>", h.onResult); err != nil {
		return err
	}
	go h.inbox()

	if h.cfg.FileSize > 0 {
		path := filepath.Join(h.cfg.DataDir, fmt.Sprintf("testfile-%dMB.bin", h.cfg.FileSize>>20))
		if st, err := os.Stat(path); err != nil || st.Size() != h.cfg.FileSize {
			if err := writeRandomFile(path, h.cfg.FileSize); err != nil {
				return err
			}
		}
		sf, err := n.Share(ctx, path, nil)
		if err != nil {
			return err
		}
		h.fileLink, h.fileSize = sf.Link, sf.Size
	}
	return nil
}

func writeRandomFile(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.CopyN(f, rand.Reader, size)
	return err
}

// Close stops everything.
func (h *Host) Close() {
	if h.http != nil {
		h.http.Close()
	}
	if h.node != nil {
		h.node.Close()
	}
	if h.relay != nil {
		h.relay.Close()
	}
	h.mu.Lock()
	for _, c := range h.clients {
		if c.file != nil {
			c.file.Close()
		}
	}
	h.mu.Unlock()
}

// Node returns the host node.
func (h *Host) Node() *node.Node { return h.node }

// RelayPort returns the embedded relay's port (0 without one).
func (h *Host) RelayPort() int {
	if h.relay == nil {
		return 0
	}
	return h.relay.Port()
}

// RelayCert returns the embedded relay's TCP certificate fingerprint.
func (h *Host) RelayCert() string { return h.relayFP }

// ===== control requests =====

func (h *Host) handleCtl(m *node.Message) ([]byte, error) {
	var r ctlReq
	if err := json.Unmarshal(m.Data, &r); err != nil {
		return nil, err
	}
	switch r.Op {
	case "hello":
		cs := h.client(m.From)
		h.mu.Lock()
		cs.Info = r.Info
		h.mu.Unlock()
		h.printf(m.From, "INFO", "hello: %s", r.Info)
		return mustJSON(hostHello{
			Host: h.cfg.ID, Version: node.ClientName, TimeNs: time.Now().UnixNano(),
			FileLink: h.fileLink, FileSize: h.fileSize, RelayCert: h.relayFP,
			RelayToken: h.relay != nil && h.cfg.Token != "", Direct: h.node.Info().Direct,
			QUIC: h.relay != nil && h.relay.QUICEnabled(),
		}), nil
	case "bulk_start":
		h.mu.Lock()
		h.bulk[m.From] = &bulkState{lastSeq: -1, started: time.Now()}
		h.mu.Unlock()
		return []byte("{}"), nil
	case "bulk_result":
		// Wait until all messages arrived or the stream went quiet.
		deadline := time.Now().Add(20 * time.Second)
		for {
			h.mu.Lock()
			b := h.bulk[m.From]
			var res bulkResult
			var quiet bool
			if b != nil {
				res = bulkResult{Count: b.count, Bytes: b.bytes, OutOfOrder: b.ooo, Ms: float64(b.last.Sub(b.first).Microseconds()) / 1000}
				quiet = !b.last.IsZero() && time.Since(b.last) > 2*time.Second || b.last.IsZero() && time.Since(b.started) > 5*time.Second
			}
			h.mu.Unlock()
			if b == nil {
				return nil, errors.New("bulk_start missing")
			}
			if res.Count >= r.N || quiet || time.Now().After(deadline) {
				return mustJSON(res), nil
			}
			time.Sleep(20 * time.Millisecond)
		}
	case "fanout":
		if r.N <= 0 || r.N > 1_000_000 || r.Size > 1<<20 || !strings.HasPrefix(r.Topic, "diag.fan.") {
			return nil, errors.New("bad fanout request")
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			payload := make([]byte, r.Size)
			for i := 0; i < r.N; i++ {
				copy(payload, strconv.Itoa(i))
				if err := h.node.Publish(ctx, r.Topic, payload); err != nil {
					return
				}
			}
		}()
		return []byte("{}"), nil
	}
	return nil, fmt.Errorf("unknown op %q", r.Op)
}

// inbox counts bulk messages and echoes datagrams.
func (h *Host) inbox() {
	for m := range h.node.Messages() {
		switch m.Kind {
		case node.KindPresence:
			if strings.HasPrefix(m.From, "diag-host") {
				continue
			}
			h.mu.Lock()
			if cs, ok := h.clients[m.From]; ok {
				cs.Online = m.Subject == "join"
			}
			known := h.clients[m.From] != nil
			h.mu.Unlock()
			if known {
				h.printf(m.From, "PRESENCE", "%s", m.Subject)
			}
		case node.KindDirect:
			if len(m.Data) == 0 {
				continue
			}
			switch m.Data[0] {
			case 'B':
				seq, _ := strconv.Atoi(string(m.Data[1:min(len(m.Data), 12)]))
				now := time.Now()
				h.mu.Lock()
				if b := h.bulk[m.From]; b != nil {
					if b.count == 0 {
						b.first = now
					}
					b.count++
					b.bytes += int64(len(m.Data))
					if seq < b.lastSeq {
						b.ooo++
					}
					b.lastSeq = seq
					b.last = now
				}
				h.mu.Unlock()
			case 'D':
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				h.node.Send(ctx, m.From, m.Data, node.Unreliable())
				cancel()
			}
		}
	}
}

// ===== pipe services =====

func (h *Host) serveSink(p *node.Pipe) {
	defer p.Close()
	p.SetDeadline(time.Now().Add(10 * time.Minute))
	sum := sha256.New()
	t0 := time.Now()
	nb, err := io.Copy(sum, p)
	el := time.Since(t0)
	if err != nil {
		h.printf(p.Peer, "WARN", "sink: %v after %d bytes", err, nb)
		return
	}
	res := sinkResult{Bytes: nb, SHA256: hex.EncodeToString(sum.Sum(nil)), Ms: float64(el.Microseconds()) / 1000}
	p.Write(append(mustJSON(res), '\n'))
	p.CloseWrite()
	io.Copy(io.Discard, p)
}

func (h *Host) serveSource(p *node.Pipe) {
	defer p.Close()
	p.SetDeadline(time.Now().Add(10 * time.Minute))
	line, err := bufio.NewReader(io.LimitReader(p, 4096)).ReadBytes('\n')
	if err != nil {
		return
	}
	var req sourceReq
	if json.Unmarshal(line, &req) != nil || req.Bytes < 0 || req.Bytes > 8<<30 {
		return
	}
	sum := sha256.New()
	w := io.MultiWriter(p, sum)
	for left := req.Bytes; left > 0; {
		chunk := h.block
		if int64(len(chunk)) > left {
			chunk = chunk[:left]
		}
		if _, err := w.Write(chunk); err != nil {
			h.printf(p.Peer, "WARN", "source: %v", err)
			return
		}
		left -= int64(len(chunk))
	}
	p.Write(sum.Sum(nil))
	p.CloseWrite()
	io.Copy(io.Discard, p)
}

// ===== log collection =====

func (h *Host) client(id string) *clientState {
	h.mu.Lock()
	defer h.mu.Unlock()
	cs := h.clients[id]
	if cs == nil {
		cs = &clientState{ID: id, FirstSeen: time.Now(), Online: true}
		name := strings.Map(func(r rune) rune {
			if r == '/' || r == '\\' || r == ':' || r == '.' {
				return '_'
			}
			return r
		}, id)
		cs.file, _ = os.OpenFile(filepath.Join(h.cfg.DataDir, "logs", name+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		h.clients[id] = cs
	}
	cs.LastSeen = time.Now()
	return cs
}

func (h *Host) onLog(m *node.Message) {
	id := strings.TrimPrefix(m.Subject, TopicLog+".")
	var lines []LogLine
	if err := json.Unmarshal(m.Data, &lines); err != nil {
		return
	}
	cs := h.client(id)
	for _, l := range lines {
		h.mu.Lock()
		cs.Lines++
		if cs.file != nil {
			cs.file.Write(append(mustJSON(map[string]any{"kind": "log", "t": l.T, "lvl": l.Level, "msg": l.Msg, "seq": l.Seq, "recv": time.Now().UnixMilli()}), '\n'))
		}
		h.mu.Unlock()
		if l.Level != "RESULT" { // results are printed from TopicResult
			h.printAt(id, l.Level, time.UnixMilli(l.T), l.Msg)
		}
	}
}

func (h *Host) onResult(m *node.Message) {
	id := strings.TrimPrefix(m.Subject, TopicResult+".")
	var r Result
	if err := json.Unmarshal(m.Data, &r); err != nil {
		return
	}
	cs := h.client(id)
	h.mu.Lock()
	cs.Results++
	if !r.OK {
		cs.Failed++
	}
	cs.results = append(cs.results, r)
	if len(cs.results) > 500 {
		cs.results = cs.results[len(cs.results)-500:]
	}
	if cs.file != nil {
		cs.file.Write(append(mustJSON(map[string]any{"kind": "result", "result": r}), '\n'))
	}
	h.mu.Unlock()
	status := "PASS"
	if !r.OK {
		status = "FAIL"
	}
	metrics, _ := json.Marshal(r.Metrics)
	msg := fmt.Sprintf("%s %s (%.0f ms) %s", status, r.Test, r.Ms, metrics)
	if r.Error != "" {
		msg += " error=" + r.Error
	}
	h.printAt(id, "RESULT", time.UnixMilli(r.Time), msg)
}

func (h *Host) printf(id, lvl, format string, args ...any) {
	h.printAt(id, lvl, time.Now(), fmt.Sprintf(format, args...))
}

func (h *Host) printAt(id, lvl string, t time.Time, msg string) {
	line := fmt.Sprintf("%s [%s] %-6s %s", t.Format("15:04:05.000"), id, lvl, msg)
	h.mu.Lock()
	if cs := h.clients[id]; cs != nil {
		cs.logs = append(cs.logs, line)
		if len(cs.logs) > 5000 {
			cs.logs = cs.logs[len(cs.logs)-5000:]
		}
	}
	h.mu.Unlock()
	h.outMu.Lock()
	fmt.Fprintln(h.out, line)
	h.outMu.Unlock()
}

// SendCommand sends a command to one client, or to all with "all".
func (h *Host) SendCommand(ctx context.Context, client string, cmd Command) error {
	if client == "" {
		client = "all"
	}
	return h.node.Publish(ctx, TopicCmd+"."+client, mustJSON(cmd))
}

// ===== HTTP API =====
//
//	GET /clients                         seen clients
//	GET /logs?client=ID&n=200            recent log lines (text)
//	GET /results?client=ID&n=100         recent results (JSON)
//	GET /cmd?client=ID|all&op=run&tests=all&repeat=1&pipe_mb=16
//	GET /cmd?client=ID&op=monitor&on=1
//	GET /status                          host status

func (h *Host) startHTTP() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/clients", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		list := make([]clientState, 0, len(h.clients))
		for _, c := range h.clients {
			list = append(list, clientState{ID: c.ID, FirstSeen: c.FirstSeen, LastSeen: c.LastSeen, Online: c.Online, Lines: c.Lines, Results: c.Results, Failed: c.Failed, Info: c.Info})
		}
		h.mu.Unlock()
		sort.Slice(list, func(i, j int) bool { return list[i].LastSeen.After(list[j].LastSeen) })
		writeJSON(w, list)
	})
	mux.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) {
		n := atoiDef(r.URL.Query().Get("n"), 200)
		h.mu.Lock()
		var lines []string
		for id, c := range h.clients {
			if q := r.URL.Query().Get("client"); q != "" && q != id {
				continue
			}
			lines = append(lines, c.logs...)
		}
		h.mu.Unlock()
		sort.Strings(lines) // lines start with the time of day
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, strings.Join(lines, "\n")+"\n")
	})
	mux.HandleFunc("/results", func(w http.ResponseWriter, r *http.Request) {
		n := atoiDef(r.URL.Query().Get("n"), 100)
		h.mu.Lock()
		var out []Result
		for id, c := range h.clients {
			if q := r.URL.Query().Get("client"); q != "" && q != id {
				continue
			}
			out = append(out, c.results...)
		}
		h.mu.Unlock()
		sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
		if len(out) > n {
			out = out[len(out)-n:]
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("/cmd", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		cmd := Command{Op: q.Get("op"), Repeat: atoiDef(q.Get("repeat"), 0), PipeMB: atoiDef(q.Get("pipe_mb"), 0), On: q.Get("on") == "1" || q.Get("on") == "true"}
		if t := q.Get("tests"); t != "" {
			cmd.Tests = strings.Split(t, ",")
		}
		if cmd.Op == "" {
			http.Error(w, "op required", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := h.SendCommand(ctx, q.Get("client"), cmd); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]any{"sent": cmd, "to": q.Get("client")})
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		st := map[string]any{"host": h.cfg.ID, "node": h.node.Info(), "file_link": h.fileLink, "relay_cert": h.relayFP, "addresses": LocalAddrs()}
		if h.relay != nil {
			st["relay"] = h.relay.Stats()
		}
		writeJSON(w, st)
	})
	ln, err := net.Listen("tcp", h.cfg.HTTP)
	if err != nil {
		return fmt.Errorf("diag http: %w", err)
	}
	h.http = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go h.http.Serve(ln)
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func atoiDef(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

// LocalAddrs lists this machine's non-loopback IPv4 addresses.
func LocalAddrs() []string {
	var out []string
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLinkLocalUnicast() {
				out = append(out, ipn.IP.String())
			}
		}
	}
	return out
}
