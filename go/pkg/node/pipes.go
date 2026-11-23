package node

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/wire"
)

// Built-in pipe services.
const (
	ServiceFile   = "mk.file"    // v2 file protocol
	ServiceFileV1 = "mk.file.v1" // v1 file protocol (JS clients via relay proxy); see relay.LegacyFileService
)

// Pipe is a reliable bidirectional byte stream to another node.
// It implements io.ReadWriteCloser plus CloseWrite and deadlines.
type Pipe struct {
	netx.Stream
	Peer    string // remote node id
	Service string
	Meta    string
	Direct  bool // true = peer-to-peer, false = through the relay
}

// PipeOption modifies OpenPipe.
type PipeOption func(*pipeOpts)

type pipeOpts struct {
	meta  string
	route Route
}

// WithMeta attaches a string the receiver sees as Pipe.Meta.
func WithMeta(meta string) PipeOption { return func(o *pipeOpts) { o.meta = meta } }

// WithRoute overrides the node's route policy for this pipe.
func WithRoute(r Route) PipeOption { return func(o *pipeOpts) { o.route = r } }

// PipeRejectedError is returned when the remote node refused the pipe.
type PipeRejectedError struct {
	Code   uint16
	Reason string
}

func (e *PipeRejectedError) Error() string { return "magnetk: pipe rejected: " + e.Reason }

// OpenPipe opens a pipe to node `to` for `service`.
func (n *Node) OpenPipe(ctx context.Context, to, service string, opts ...PipeOption) (*Pipe, error) {
	o := pipeOpts{route: n.cfg.Route}
	for _, f := range opts {
		f(&o)
	}
	if to == n.ID() {
		return nil, errors.New("magnetk: cannot open a pipe to yourself")
	}
	if o.route != RouteRelay {
		p, err := n.openDirect(ctx, to, service, o.meta, o.route == RouteDirect)
		if err == nil {
			return p, nil
		}
		var rej *PipeRejectedError
		if o.route == RouteDirect || errors.As(err, &rej) {
			return nil, err
		}
		n.log.Printf("[node] direct pipe to %s unavailable (%v); using relay", to, err)
	}
	return n.openViaRelay(ctx, to, service, o.meta)
}

func (n *Node) openViaRelay(ctx context.Context, to, service, meta string) (*Pipe, error) {
	ctx, cancel := n.withTimeout(ctx)
	defer cancel()
	rc, err := n.conn(ctx)
	if err != nil {
		return nil, err
	}
	st, err := rc.conn.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	po := wire.PipeOpen{To: to, Service: service, Meta: meta}
	if _, err := st.Write(po.Encode()); err != nil {
		st.Close()
		return nil, err
	}
	if err := readPipeAnswer(ctx, st); err != nil {
		st.Close()
		return nil, err
	}
	return &Pipe{Stream: st, Peer: to, Service: service, Meta: meta}, nil
}

func readPipeAnswer(ctx context.Context, st netx.Stream) error {
	if dl, ok := ctx.Deadline(); ok {
		st.SetReadDeadline(dl)
		defer st.SetReadDeadline(time.Time{})
	}
	typ, body, err := wire.ReadFrameExact(st, 64<<10)
	if err != nil {
		return fmt.Errorf("magnetk: pipe handshake: %w", err)
	}
	switch typ {
	case wire.TPipeAccept:
		return nil
	case wire.TPipeReject:
		rj, _ := wire.DecodePipeReject(body)
		if rj.Code == wire.ErrCodeNoRoute {
			return fmt.Errorf("%w: %s", ErrNoRoute, rj.Reason)
		}
		return &PipeRejectedError{Code: rj.Code, Reason: rj.Reason}
	default:
		return fmt.Errorf("magnetk: pipe handshake: unexpected frame 0x%02x", typ)
	}
}

// HandlePipe registers a handler for incoming pipes of a service. The
// handler owns the pipe and must close it.
func (n *Node) HandlePipe(service string, h func(*Pipe)) {
	n.mu.Lock()
	if h == nil {
		delete(n.services, service)
	} else {
		n.services[service] = h
	}
	n.mu.Unlock()
}

// AcceptPipe returns the next incoming pipe for a service without handler.
// Until AcceptPipe has been called once, such pipes are rejected.
func (n *Node) AcceptPipe(ctx context.Context) (*Pipe, error) {
	n.acceptingPipes.Store(true)
	select {
	case p := <-n.pipeQ:
		return p, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-n.ctx.Done():
		return nil, ErrClosed
	}
}

// ===== incoming streams =====

func (n *Node) acceptRelayStreams(rc *relayConn) {
	ctx, cancel := context.WithCancel(n.ctx)
	defer cancel()
	go func() {
		select {
		case <-rc.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	for {
		st, err := rc.conn.AcceptStream(ctx)
		if err != nil {
			return
		}
		go n.handleIncoming(st, false)
	}
}

func (n *Node) serveDirectConn(c netx.Conn) {
	defer c.Close()
	for {
		st, err := c.AcceptStream(n.ctx)
		if err != nil {
			return
		}
		go n.handleIncoming(st, true)
	}
}

func (n *Node) handleIncoming(st netx.Stream, direct bool) {
	st.SetReadDeadline(time.Now().Add(15 * time.Second))
	typ, body, err := wire.ReadFrameExact(st, 64<<10)
	st.SetReadDeadline(time.Time{})
	if err != nil || typ != wire.TPipeIncoming {
		st.Close()
		return
	}
	pi, err := wire.DecodePipeIncoming(body)
	if err != nil {
		st.Close()
		return
	}
	reject := func(code uint16, reason string) {
		r := wire.PipeReject{Code: code, Reason: reason}
		st.SetWriteDeadline(time.Now().Add(2 * time.Second))
		st.Write(r.Encode())
		st.CloseWrite()
		st.Close()
	}
	if direct && n.cfg.Token != "" && subtle.ConstantTimeCompare([]byte(pi.Token), []byte(n.cfg.Token)) != 1 {
		reject(wire.ErrCodeUnauthorized, "direct pipes require the network token")
		return
	}
	p := &Pipe{Stream: st, Peer: pi.From, Service: pi.Service, Meta: pi.Meta, Direct: direct}

	switch pi.Service {
	case ServiceFile, ServiceFileV1:
		if _, err := st.Write(wire.EncodePipeAccept()); err != nil {
			st.Close()
			return
		}
		if pi.Service == ServiceFile {
			n.store.Serve(st)
		} else {
			n.store.ServeV1(st)
		}
		st.CloseWrite()
		st.Close()
		return
	}

	n.mu.RLock()
	h := n.services[pi.Service]
	n.mu.RUnlock()
	if h != nil {
		if _, err := st.Write(wire.EncodePipeAccept()); err != nil {
			st.Close()
			return
		}
		h(p)
		return
	}
	if !n.acceptingPipes.Load() {
		reject(wire.ErrCodeNotFound, "no handler for service "+pi.Service)
		return
	}
	if _, err := st.Write(wire.EncodePipeAccept()); err != nil {
		st.Close()
		return
	}
	select {
	case n.pipeQ <- p:
	case <-time.After(10 * time.Second):
		st.Close() // nobody is accepting
	case <-n.ctx.Done():
		st.Close()
	}
}

// serveLegacyDirect serves the v1 file protocol to old clients (JS SDK)
// that connect to the direct port with plain TCP.
func (n *Node) serveLegacyDirect(c net.Conn) {
	defer c.Close()
	n.store.ServeV1(c)
}

// ===== direct connections =====

type directCache struct {
	n      *Node
	mu     sync.Mutex
	conns  map[string]netx.Conn // node id -> live connection
	failed map[string]time.Time // node id -> last failure (negative cache)
	dialMu map[string]*sync.Mutex
}

func newDirectCache(n *Node) *directCache {
	return &directCache{n: n, conns: map[string]netx.Conn{}, failed: map[string]time.Time{}, dialMu: map[string]*sync.Mutex{}}
}

func (d *directCache) closeAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, c := range d.conns {
		c.Close()
		delete(d.conns, id)
	}
}

// reset drops all cached connections and forgets past failures (the network
// changed, so both may be stale).
func (d *directCache) reset() {
	d.closeAll()
	d.mu.Lock()
	clear(d.failed)
	d.mu.Unlock()
}

func (d *directCache) get(id string) netx.Conn {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.conns[id]
	if c == nil {
		return nil
	}
	select {
	case <-c.Done():
		delete(d.conns, id)
		return nil
	default:
		return c
	}
}

func (n *Node) openDirect(ctx context.Context, to, service, meta string, force bool) (*Pipe, error) {
	d := n.direct
	c := d.get(to)
	if c == nil {
		d.mu.Lock()
		if t, ok := d.failed[to]; ok && !force && time.Since(t) < time.Minute {
			d.mu.Unlock()
			return nil, errors.New("direct path failed recently")
		}
		mu := d.dialMu[to]
		if mu == nil {
			mu = &sync.Mutex{}
			d.dialMu[to] = mu
		}
		d.mu.Unlock()

		mu.Lock() // one dial per peer at a time; parallel callers reuse it
		c = d.get(to)
		if c == nil {
			var err error
			c, err = n.dialDirect(ctx, to)
			d.mu.Lock()
			if err != nil {
				d.failed[to] = time.Now()
			} else {
				delete(d.failed, to)
				d.conns[to] = c
			}
			d.mu.Unlock()
			if err != nil {
				mu.Unlock()
				return nil, err
			}
		}
		mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(ctx, n.cfg.DirectTimeout+5*time.Second)
	defer cancel()
	st, err := c.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	pi := wire.PipeIncoming{From: n.ID(), Service: service, Meta: meta, Via: wire.ViaDirect, Token: n.cfg.Token}
	if _, err := st.Write(pi.Encode()); err != nil {
		st.Close()
		return nil, err
	}
	if err := readPipeAnswer(ctx, st); err != nil {
		st.Close()
		return nil, err
	}
	return &Pipe{Stream: st, Peer: to, Service: service, Meta: meta, Direct: true}, nil
}

// dialDirect races the peer's advertised addresses.
func (n *Node) dialDirect(ctx context.Context, to string) (netx.Conn, error) {
	info, err := n.Lookup(ctx, to)
	if err != nil {
		return nil, err
	}
	cands := directCandidates(info)
	if len(cands) == 0 {
		return nil, errors.New("peer accepts no direct connections")
	}
	ctx, cancel := context.WithTimeout(ctx, n.cfg.DirectTimeout)
	defer cancel()
	type res struct {
		c   netx.Conn
		err error
	}
	ch := make(chan res, len(cands))
	for _, a := range cands {
		go func(a string) {
			c, err := n.ep.Dial(ctx, a)
			ch <- res{c, err}
		}(a)
	}
	var winner netx.Conn
	var errs []error
	for range cands {
		r := <-ch
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		if winner == nil {
			winner = r.c
			cancel() // stop the others
		} else {
			r.c.Close()
		}
	}
	if winner == nil {
		return nil, fmt.Errorf("no direct address of %s reachable: %w", to, errors.Join(errs...))
	}
	return winner, nil
}

// directCandidates are the advertised addresses plus the address the relay
// observed combined with each advertised port (helps behind port-preserving
// NAT or on VPSes that did not advertise their public IP).
func directCandidates(info *wire.NodeInfo) []string {
	seen := map[string]bool{}
	var out []string
	add := func(a string) {
		if a != "" && !seen[a] && len(out) < 8 {
			seen[a] = true
			out = append(out, a)
		}
	}
	for _, a := range info.Addrs {
		add(a)
	}
	if host, _, err := net.SplitHostPort(info.Observed); err == nil {
		for _, a := range info.Addrs {
			if _, port, err := net.SplitHostPort(a); err == nil {
				add(net.JoinHostPort(host, port))
			}
		}
	}
	return out
}

func timeUntilMs(t time.Time) float64 {
	return float64(time.Until(t).Milliseconds())
}
