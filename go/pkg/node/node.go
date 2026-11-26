package node

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iMarand/magnetk/go/pkg/files"
	"github.com/iMarand/magnetk/go/pkg/hrtime"
	"github.com/iMarand/magnetk/go/pkg/identity"
	"github.com/iMarand/magnetk/go/pkg/magnetk"
	magnetkNAT "github.com/iMarand/magnetk/go/pkg/nat"
	magnetkNet "github.com/iMarand/magnetk/go/pkg/network"
	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/wire"
)

// ClientName is sent to relays in HELLO.
var ClientName = "magnetk-go/2.0"

// Node is a Magnetk client.
type Node struct {
	cfg Config
	log *log.Logger
	ep  *netx.Endpoint

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu         sync.RWMutex
	cur        *relayConn
	ready      chan struct{} // closed while connected
	kick       chan struct{} // Reconnect: skip the current retry backoff
	inboxSlow  atomic.Bool   // inbox consumer is behind (see offer)
	fatal      error
	id         string
	subs       map[*Subscription]struct{}
	services   map[string]func(*Pipe)
	reqHandler func(*Message) ([]byte, error)
	sysReq     map[string]func(*Message) ([]byte, error) // payload prefix -> handler

	inbox          chan *Message
	pipeQ          chan *Pipe
	acceptingPipes atomic.Bool
	nextID         atomic.Uint64
	pending        sync.Map // id -> chan wire.Reply
	acks           sync.Map // id -> chan wire.Ack
	queries        sync.Map // id -> chan query
	pings          sync.Map // id -> chan struct{}

	store    *files.Store
	directLn netx.Listener
	direct   *directCache
	natMaps  []*magnetkNAT.Mapping
	advAddrs []string

	stats     nodeStats
	closeOnce sync.Once
}

type query struct {
	typ  uint8
	body []byte
	err  error
}

type nodeStats struct {
	sent, received, dropped, reconnects atomic.Uint64
}

type relayConn struct {
	addr     string
	conn     netx.Conn
	ctrl     netx.Stream
	w        *wire.QueueWriter
	welcome  wire.Welcome
	done     chan struct{}
	lastRecv atomic.Int64 // unix nanos of the last frame from the relay
}

// New creates a node. Call Start to connect.
func New(cfg Config) (*Node, error) {
	cfg.defaults()
	if len(cfg.Relays) == 0 {
		return nil, errors.New("magnetk: at least one relay address is required")
	}
	relays := make([]string, 0, len(cfg.Relays))
	for _, r := range cfg.Relays {
		if a := magnetk.NormalizeRelay(r); a != "" {
			relays = append(relays, a)
		}
	}
	if len(relays) == 0 {
		return nil, fmt.Errorf("magnetk: no usable relay address in %v", cfg.Relays)
	}
	cfg.Relays = relays
	logger := cfg.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}

	n := &Node{
		cfg:      cfg,
		log:      logger,
		ready:    make(chan struct{}),
		kick:     make(chan struct{}, 1),
		subs:     make(map[*Subscription]struct{}),
		services: make(map[string]func(*Pipe)),
		inbox:    make(chan *Message, cfg.InboxSize),
		pipeQ:    make(chan *Pipe, 64),
		store:    newStore(cfg.NoCompression),
		id:       cfg.ID,
	}
	n.direct = newDirectCache(n)
	n.ctx, n.cancel = context.WithCancel(context.Background())

	ep, err := netx.NewEndpoint(netx.Options{
		Mode:       cfg.Transport,
		QUIC:       cfg.QUIC,
		CertFile:   cfg.CertFile,
		KeyFile:    cfg.KeyFile,
		VerifyPeer: cfg.VerifyPeer,
		CAFile:     cfg.CAFile,
		Legacy:     n.serveLegacyDirect,
		Logger:     logger,
	})
	if err != nil {
		return nil, err
	}
	n.ep = ep

	if cfg.Listen {
		if err := n.startDirectListener(); err != nil {
			ep.Close()
			return nil, err
		}
	}
	return n, nil
}

// Start connects to a relay, waiting until the first connection succeeds or
// ctx ends. Reconnection then happens in the background.
func (n *Node) Start(ctx context.Context) error {
	n.wg.Add(1)
	go n.run()
	if err := n.WaitConnected(ctx); err != nil {
		n.mu.RLock()
		fatal := n.fatal
		n.mu.RUnlock()
		if fatal != nil {
			return fatal
		}
		return err
	}
	return nil
}

// ID returns the node id (assigned by the relay if none was configured).
func (n *Node) ID() string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.id
}

// Connected reports whether a relay connection is up.
func (n *Node) Connected() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.cur != nil
}

// WaitConnected blocks until a relay connection is up.
func (n *Node) WaitConnected(ctx context.Context) error {
	for {
		n.mu.RLock()
		ready, fatal := n.ready, n.fatal
		connected := n.cur != nil
		n.mu.RUnlock()
		if connected {
			return nil
		}
		if fatal != nil {
			return fatal
		}
		select {
		case <-ready:
		case <-n.ctx.Done():
			return ErrClosed
		case <-ctx.Done():
			return fmt.Errorf("%w: %v", ErrNotConnected, ctx.Err())
		}
	}
}

// Info describes the current relay connection.
type Info struct {
	NodeID    string
	Connected bool
	Relay     string
	RelayID   string
	Protocol  string
	Observed  string
	RTT       time.Duration
	Direct    []string // advertised direct addresses
}

// Key returns the node's identity key (nil if it has none).
func (n *Node) Key() *identity.Key { return n.cfg.Key }

// Info returns the current connection details.
func (n *Node) Info() Info {
	n.mu.RLock()
	defer n.mu.RUnlock()
	in := Info{NodeID: n.id, Direct: n.advAddrs}
	if rc := n.cur; rc != nil {
		in.Connected = true
		in.Relay = rc.addr
		in.RelayID = rc.welcome.RelayID
		in.Protocol = rc.conn.Protocol()
		in.Observed = rc.welcome.Observed
		in.RTT = rc.conn.RTT()
	}
	return in
}

// Close disconnects and releases everything.
func (n *Node) Close() error {
	n.closeOnce.Do(func() {
		n.mu.RLock()
		rc := n.cur
		n.mu.RUnlock()
		if rc != nil {
			rc.w.TrySend(wire.NewEnc(wire.TBye, 0).Frame())
			rc.ctrl.SetWriteDeadline(time.Now().Add(2 * time.Second))
			rc.w.Close()
		}
		n.cancel()
		if rc != nil {
			rc.conn.Close()
		}
		if n.directLn != nil {
			n.directLn.Close()
		}
		n.direct.closeAll()
		for _, m := range n.natMaps {
			m.Close()
		}
		n.wg.Wait()
		n.mu.Lock()
		for s := range n.subs {
			s.stop()
		}
		n.mu.Unlock()
		n.store.Close()
		n.ep.Close()
	})
	return nil
}

// Reconnect drops the current relay connection (and cached peer-to-peer
// connections) and connects again immediately. Call it when the device's
// network changed (Wi-Fi <-> mobile data, VPN up/down): the old sockets are
// usually dead but would otherwise only be detected by the heartbeat.
func (n *Node) Reconnect() {
	n.mu.RLock()
	rc := n.cur
	n.mu.RUnlock()
	n.direct.reset()
	select {
	case n.kick <- struct{}{}:
	default:
	}
	if rc != nil {
		n.log.Printf("[node] reconnect requested")
		rc.conn.Close()
	}
}

func (n *Node) emit(ev Event) {
	if n.cfg.OnEvent != nil {
		n.cfg.OnEvent(ev)
	}
}

// ===== connection management =====

func (n *Node) run() {
	defer n.wg.Done()
	backoff := n.cfg.ReconnectMin
	for {
		if n.ctx.Err() != nil {
			return
		}
		rc, err := n.dialAny()
		if err != nil {
			if errors.Is(err, ErrUnauthorized) {
				n.mu.Lock()
				n.fatal = err
				close(n.ready)
				n.ready = make(chan struct{})
				n.mu.Unlock()
				n.emit(Event{Type: EventError, Err: err})
				return
			}
			n.log.Printf("[node] connect failed: %v (retry in %v)", err, backoff)
			jitter := time.Duration(rand.Int63n(int64(backoff)/4 + 1))
			select {
			case <-time.After(backoff + jitter):
			case <-n.kick:
				backoff = n.cfg.ReconnectMin
				continue
			case <-n.ctx.Done():
				return
			}
			backoff *= 2
			if backoff > n.cfg.ReconnectMax {
				backoff = n.cfg.ReconnectMax
			}
			continue
		}
		backoff = n.cfg.ReconnectMin
		n.attach(rc)
		n.emit(Event{Type: EventConnected, Relay: rc.addr, RelayID: rc.welcome.RelayID, Protocol: rc.conn.Protocol()})
		n.serve(rc)
		n.detach(rc)
		if n.ctx.Err() != nil {
			return
		}
		n.stats.reconnects.Add(1)
		n.emit(Event{Type: EventDisconnected, Relay: rc.addr, Err: ErrDisconnected})
	}
}

// relayStagger is how long dialAny waits for a relay before also trying
// the next one in the list.
const relayStagger = 400 * time.Millisecond

// dialAny connects to the first relay that answers, preferring the order
// of Config.Relays: the next relay is tried when the previous one failed
// or has not answered within relayStagger (e.g. a LAN relay while the
// phone is on mobile data), so an unreachable entry costs at most 400ms.
func (n *Node) dialAny() (*relayConn, error) {
	relays := n.cfg.Relays
	ctx, cancel := context.WithTimeout(n.ctx, 15*time.Second)
	defer cancel()
	if len(relays) == 1 {
		rc, err := n.dialRelay(ctx, relays[0])
		if err != nil && !errors.Is(err, ErrUnauthorized) {
			err = fmt.Errorf("%s: %w", relays[0], err)
		}
		return rc, err
	}
	type result struct {
		addr string
		rc   *relayConn
		err  error
	}
	ch := make(chan result, len(relays))
	started, pending := 0, 0
	startNext := func() {
		addr := relays[started]
		started++
		pending++
		go func() {
			rc, err := n.dialRelay(ctx, addr)
			ch <- result{addr, rc, err}
		}()
	}
	// drain closes connections that finished after a winner was chosen.
	drain := func(left int) {
		go func() {
			for ; left > 0; left-- {
				if r := <-ch; r.rc != nil {
					r.rc.conn.Close()
				}
			}
		}()
	}
	startNext()
	stagger := time.NewTimer(relayStagger)
	defer stagger.Stop()
	var errs []error
	for pending > 0 {
		select {
		case r := <-ch:
			pending--
			if r.err == nil {
				cancel()
				drain(pending)
				return r.rc, nil
			}
			if errors.Is(r.err, ErrUnauthorized) {
				cancel()
				drain(pending)
				return nil, r.err
			}
			errs = append(errs, fmt.Errorf("%s: %w", r.addr, r.err))
			if started < len(relays) {
				startNext()
				stagger.Reset(relayStagger)
			}
		case <-stagger.C:
			if started < len(relays) {
				startNext()
				stagger.Reset(relayStagger)
			}
		}
	}
	return nil, errors.Join(errs...)
}

func (n *Node) dialRelay(ctx context.Context, addr string) (*relayConn, error) {
	c, err := n.ep.Dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	ctrl, err := c.OpenStream(ctx)
	if err != nil {
		c.Close()
		return nil, err
	}
	flags := uint32(0)
	if n.cfg.WatchPresence {
		flags |= wire.HelloWatchPresence
	}
	n.mu.RLock()
	id := n.id
	addrs := n.advAddrs
	n.mu.RUnlock()
	hello := wire.Hello{Version: wire.Version, NodeID: id, Token: n.cfg.Token, Flags: flags, Meta: n.cfg.Meta, Client: ClientName, Addrs: addrs}
	if k := n.cfg.Key; k != nil {
		hello.PubKey, hello.TS = k.Public(), uint64(time.Now().UnixMilli())
		hello.Sig = k.Sign(identity.HelloPayload(id, hello.TS))
	}
	if _, err := ctrl.Write(hello.Encode()); err != nil {
		c.Close()
		return nil, err
	}
	ctrl.SetReadDeadline(time.Now().Add(15 * time.Second))
	typ, body, err := wire.ReadFrameExact(ctrl, 64<<10)
	ctrl.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("handshake: %w", err)
	}
	if typ == wire.TError {
		e, _ := wire.DecodeError(body)
		c.Close()
		if e.Code == wire.ErrCodeUnauthorized {
			if e.Msg != "" && e.Msg != "invalid token" {
				return nil, fmt.Errorf("%w: %s", ErrUnauthorized, e.Msg)
			}
			return nil, ErrUnauthorized
		}
		return nil, fmt.Errorf("relay refused: %s", e.Msg)
	}
	if typ != wire.TWelcome {
		c.Close()
		return nil, fmt.Errorf("handshake: unexpected frame 0x%02x", typ)
	}
	w, err := wire.DecodeWelcome(body)
	if err != nil {
		c.Close()
		return nil, err
	}
	return &relayConn{addr: addr, conn: c, ctrl: ctrl, w: wire.NewQueueWriter(ctrl, n.cfg.SendQueueBytes), welcome: w, done: make(chan struct{})}, nil
}

// attach makes rc current and restores subscriptions and shared files.
func (n *Node) attach(rc *relayConn) {
	n.mu.Lock()
	n.id = rc.welcome.NodeID
	subs := make([]*Subscription, 0, len(n.subs))
	for s := range n.subs {
		subs = append(subs, s)
	}
	n.mu.Unlock()

	for _, s := range subs {
		m := wire.Sub{Topic: s.Pattern, Queue: s.Queue}
		rc.w.TrySend(m.Encode())
	}
	for _, m := range n.store.List() {
		rc.w.TrySend(n.publishFrame(m))
	}

	n.mu.Lock()
	n.cur = rc
	close(n.ready)
	n.mu.Unlock()
	n.log.Printf("[node] %s connected to relay %s (%s) via %s", rc.welcome.NodeID, rc.welcome.RelayID, rc.addr, rc.conn.Protocol())
}

func (n *Node) detach(rc *relayConn) {
	n.mu.Lock()
	if n.cur == rc {
		n.cur = nil
		n.ready = make(chan struct{})
	}
	n.mu.Unlock()
	rc.ctrl.SetWriteDeadline(time.Now().Add(time.Second))
	rc.w.Close()
	rc.conn.Close()
	close(rc.done)
	// Fail everything that was waiting on this relay.
	n.pending.Range(func(k, v any) bool {
		select {
		case v.(chan wire.Reply) <- wire.Reply{Status: 255}:
		default:
		}
		return true
	})
	n.acks.Range(func(k, v any) bool {
		select {
		case v.(chan wire.Ack) <- wire.Ack{Status: 255}:
		default:
		}
		return true
	})
	n.queries.Range(func(k, v any) bool {
		select {
		case v.(chan query) <- query{err: ErrDisconnected}:
		default:
		}
		return true
	})
}

// conn returns the current relay connection, waiting up to ctx for one.
func (n *Node) conn(ctx context.Context) (*relayConn, error) {
	for {
		n.mu.RLock()
		rc, ready, fatal := n.cur, n.ready, n.fatal
		n.mu.RUnlock()
		if rc != nil {
			return rc, nil
		}
		if fatal != nil {
			return nil, fatal
		}
		select {
		case <-ready:
		case <-n.ctx.Done():
			return nil, ErrClosed
		case <-ctx.Done():
			return nil, ErrNotConnected
		}
	}
}

// send queues a control frame, waiting for queue space (backpressure).
func (n *Node) send(ctx context.Context, frame []byte) (*relayConn, error) {
	rc, err := n.conn(ctx)
	if err != nil {
		return nil, err
	}
	if err := rc.w.Send(ctx, frame); err != nil {
		if errors.Is(err, wire.ErrWriterClosed) {
			return nil, ErrDisconnected
		}
		return nil, err
	}
	n.stats.sent.Add(1)
	return rc, nil
}

func (n *Node) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, n.cfg.RequestTimeout)
}

// serve runs the reader for one relay connection until it fails.
func (n *Node) serve(rc *relayConn) {
	go n.acceptRelayStreams(rc)
	go n.datagramLoop(rc)
	go n.heartbeat(rc)
	rc.lastRecv.Store(time.Now().UnixNano())
	r := bufio.NewReaderSize(rc.ctrl, 64<<10)
	maxFrame := int(rc.welcome.MaxFrame) + 4096
	if rc.welcome.MaxFrame == 0 {
		maxFrame = wire.DefaultMaxFrame
	}
	for {
		typ, body, err := wire.ReadFrame(r, maxFrame)
		if err != nil {
			if n.ctx.Err() == nil {
				n.log.Printf("[node] relay connection lost: %v", err)
			}
			return
		}
		rc.lastRecv.Store(time.Now().UnixNano())
		n.dispatch(typ, body, false)
	}
}

// heartbeat pings the relay and drops the connection when the relay stops
// answering (crashed VPS, dead NAT mapping, cable pulled...).
func (n *Node) heartbeat(rc *relayConn) {
	t := time.NewTicker(n.cfg.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-rc.done:
			return
		case <-n.ctx.Done():
			return
		case <-t.C:
			silent := time.Since(time.Unix(0, rc.lastRecv.Load()))
			if silent > n.cfg.HeartbeatTimeout {
				n.log.Printf("[node] relay %s silent for %v; reconnecting", rc.addr, silent.Round(time.Millisecond))
				rc.conn.Close()
				return
			}
			rc.w.TrySend(wire.EncodePing(0))
		}
	}
}

func (n *Node) datagramLoop(rc *relayConn) {
	if rc.conn.Protocol() != "quic" {
		return
	}
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
		d, err := rc.conn.ReceiveDatagram(ctx)
		if err != nil {
			return
		}
		if len(d) < wire.HeaderSize || int(binary.BigEndian.Uint32(d)) != len(d)-wire.HeaderSize {
			continue
		}
		n.dispatch(d[4], d[wire.HeaderSize:], true)
	}
}

func (n *Node) dispatch(typ uint8, body []byte, datagram bool) {
	switch typ {
	case wire.TDeliver:
		d, err := wire.DecodeDeliver(body)
		if err != nil {
			return
		}
		n.stats.received.Add(1)
		n.deliver(d, datagram)
	case wire.TReply:
		r, err := wire.DecodeReply(body)
		if err != nil {
			return
		}
		if ch, ok := n.pending.Load(r.ID); ok {
			select {
			case ch.(chan wire.Reply) <- r:
			default:
			}
		}
	case wire.TAck:
		a, err := wire.DecodeAck(body)
		if err != nil {
			return
		}
		if ch, ok := n.acks.Load(a.ID); ok {
			select {
			case ch.(chan wire.Ack) <- a:
			default:
			}
		}
	case wire.TLookupRes, wire.TFileProviders, wire.TStatsRes:
		if len(body) < 8 {
			return
		}
		id := binary.BigEndian.Uint64(body)
		if ch, ok := n.queries.Load(id); ok {
			select {
			case ch.(chan query) <- query{typ: typ, body: body}:
			default:
			}
		}
	case wire.TPong:
		if v, err := wire.DecodeU64(body); err == nil && v != 0 {
			if ch, ok := n.pings.LoadAndDelete(v); ok {
				close(ch.(chan struct{}))
			}
		}
	case wire.TPing:
		if v, err := wire.DecodeU64(body); err == nil {
			n.mu.RLock()
			rc := n.cur
			n.mu.RUnlock()
			if rc != nil {
				rc.w.TrySend(wire.EncodePong(v))
			}
		}
	case wire.TPresence:
		p, err := wire.DecodePresence(body)
		if err != nil {
			return
		}
		ev := "join"
		if p.Event == wire.PresenceLeave {
			ev = "leave"
		}
		n.toInbox(&Message{Kind: KindPresence, From: p.NodeID, Subject: ev, Data: []byte(p.Meta), node: n})
	case wire.TError:
		e, err := wire.DecodeError(body)
		if err != nil {
			return
		}
		if e.Ref != 0 {
			if ch, ok := n.acks.Load(e.Ref); ok {
				select {
				case ch.(chan wire.Ack) <- wire.Ack{ID: e.Ref, Status: wire.AckRejected, Detail: e.Msg}:
				default:
				}
				return
			}
			if ch, ok := n.pending.Load(e.Ref); ok {
				select {
				case ch.(chan wire.Reply) <- wire.Reply{ID: e.Ref, Status: wire.ReplyError, Payload: []byte(e.Msg)}:
				default:
				}
				return
			}
		}
		n.log.Printf("[node] relay error %d: %s", e.Code, e.Msg)
		n.emit(Event{Type: EventError, Err: fmt.Errorf("relay error %d: %s", e.Code, e.Msg)})
	}
}

// query sends a frame and waits for the response with the same id.
func (n *Node) query(ctx context.Context, id uint64, frame []byte) (query, error) {
	ctx, cancel := n.withTimeout(ctx)
	defer cancel()
	ch := make(chan query, 1)
	n.queries.Store(id, ch)
	defer n.queries.Delete(id)
	if _, err := n.send(ctx, frame); err != nil {
		return query{}, err
	}
	select {
	case q := <-ch:
		return q, q.err
	case <-ctx.Done():
		return query{}, ErrTimeout
	case <-n.ctx.Done():
		return query{}, ErrClosed
	}
}

// Ping measures the round trip to the relay through the control stream.
func (n *Node) Ping(ctx context.Context) (time.Duration, error) {
	ctx, cancel := n.withTimeout(ctx)
	defer cancel()
	id := n.nextID.Add(1)
	ch := make(chan struct{})
	n.pings.Store(id, ch)
	defer n.pings.Delete(id)
	start := hrtime.Now()
	if _, err := n.send(ctx, wire.EncodePing(id)); err != nil {
		return 0, err
	}
	select {
	case <-ch:
		return hrtime.Since(start), nil
	case <-ctx.Done():
		return 0, ErrTimeout
	}
}

// Flush waits until the relay has processed everything this node sent
// before the call (one round trip on the control stream, which the relay
// handles in order). Subscribe and Share call it so their effect is
// visible to other nodes when they return.
func (n *Node) Flush(ctx context.Context) error {
	_, err := n.Ping(ctx)
	return err
}

// Lookup returns what the relay knows about a node.
func (n *Node) Lookup(ctx context.Context, nodeID string) (*wire.NodeInfo, error) {
	id := n.nextID.Add(1)
	l := wire.Lookup{ID: id, Kind: wire.LookupNode, Key: nodeID}
	q, err := n.query(ctx, id, l.Encode())
	if err != nil {
		return nil, err
	}
	res, err := wire.DecodeLookupRes(q.body)
	if err != nil {
		return nil, err
	}
	if len(res.Nodes) == 0 {
		return nil, ErrNoRoute
	}
	return &res.Nodes[0], nil
}

// ListNodes lists connected nodes whose id starts with prefix ("" = all, max 1000).
func (n *Node) ListNodes(ctx context.Context, prefix string) ([]wire.NodeInfo, error) {
	id := n.nextID.Add(1)
	l := wire.Lookup{ID: id, Kind: wire.LookupPrefix, Key: prefix}
	q, err := n.query(ctx, id, l.Encode())
	if err != nil {
		return nil, err
	}
	res, err := wire.DecodeLookupRes(q.body)
	return res.Nodes, err
}

// RelayStats returns the relay's statistics as JSON.
func (n *Node) RelayStats(ctx context.Context) ([]byte, error) {
	id := n.nextID.Add(1)
	q, err := n.query(ctx, id, wire.EncodeStatsReq(id))
	if err != nil {
		return nil, err
	}
	_, js, err := wire.DecodeStatsRes(q.body)
	return js, err
}

// Stats are node-side counters.
type Stats struct {
	Sent, Received, Dropped, Reconnects uint64
	ServedChunks, ServedBytes           uint64
	SharedFiles                         int
}

// Stats returns node-side counters.
func (n *Node) Stats() Stats {
	return Stats{
		Sent:         n.stats.sent.Load(),
		Received:     n.stats.received.Load(),
		Dropped:      n.stats.dropped.Load(),
		Reconnects:   n.stats.reconnects.Load(),
		ServedChunks: n.store.ServedChunks.Load(),
		ServedBytes:  n.store.ServedBytes.Load(),
		SharedFiles:  len(n.store.List()),
	}
}

// ===== direct listener =====

func (n *Node) startDirectListener() error {
	host, portStr, err := net.SplitHostPort(n.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("magnetk: listen address %q: %w", n.cfg.ListenAddr, err)
	}
	port, _ := strconv.Atoi(portStr)
	ln, err := n.ep.Listen(host, port)
	if err != nil {
		return err
	}
	n.directLn = ln
	p := ln.Port()

	var addrs []string
	seen := map[string]bool{}
	add := func(a string) {
		if a != "" && !seen[a] {
			seen[a] = true
			addrs = append(addrs, a)
		}
	}
	for _, a := range n.cfg.AdvertiseAddrs {
		add(a)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		for _, ip := range magnetkNet.LANIPs() {
			add(net.JoinHostPort(ip, strconv.Itoa(p)))
		}
	} else {
		add(net.JoinHostPort(host, strconv.Itoa(p)))
	}
	if n.cfg.UPnP {
		n.natMaps = magnetkNAT.Open(p, "magnetk-node")
		for _, m := range n.natMaps {
			if m.ExternalIP != nil {
				add(net.JoinHostPort(m.ExternalIP.String(), strconv.Itoa(m.ExternalPort)))
			}
		}
	}
	n.advAddrs = addrs

	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		for {
			c, err := ln.Accept(n.ctx)
			if err != nil {
				return
			}
			go n.serveDirectConn(c)
		}
	}()
	n.log.Printf("[node] direct listener on port %d, advertising %v", p, addrs)
	return nil
}

// DirectPort returns the direct listener port (0 when not listening).
func (n *Node) DirectPort() int {
	if n.directLn == nil {
		return 0
	}
	return n.directLn.Port()
}

// Security describes the encryption of the current relay connection.
func (n *Node) Security() netx.SecurityInfo {
	n.mu.RLock()
	rc := n.cur
	n.mu.RUnlock()
	if rc == nil {
		return netx.SecurityInfo{}
	}
	return netx.Security(rc.conn)
}
