package relay

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/wire"
)

// Server is a running relay.
type Server struct {
	cfg     Config
	log     *log.Logger
	ep      *netx.Endpoint
	ln      netx.Listener
	legacy  net.Listener
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	started time.Time

	ids *identities

	mu       sync.RWMutex
	sessions map[string]*session    // nodes attached to this relay
	routes   map[string]*remoteNode // nodes attached to federated relays
	links    map[string]*link       // relay id -> link

	subs    *subIndex
	files   *fileRegistry
	offline *offlineStore

	pipes   atomic.Int64
	bufPool sync.Pool
	st      counters
}

type remoteNode struct {
	info wire.NodeInfo
	link *link
}

// New creates a relay; call Start to begin serving.
func New(cfg Config) (*Server, error) {
	cfg.defaults()
	if cfg.ID == "" {
		cfg.ID = "relay-" + randomID(6)
	}
	if !validNodeID(cfg.ID) {
		return nil, fmt.Errorf("relay: invalid relay id %q", cfg.ID)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	ids, err := openIdentities(cfg.IdentitiesFile)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		ids:      ids,
		cfg:      cfg,
		log:      logger,
		ctx:      ctx,
		cancel:   cancel,
		sessions: make(map[string]*session),
		routes:   make(map[string]*remoteNode),
		links:    make(map[string]*link),
		subs:     newSubIndex(),
		files:    newFileRegistry(),
		offline:  newOfflineStore(cfg.OfflineMaxMsgs, cfg.OfflineMaxBytes, cfg.OfflineTTL),
	}
	bufSize := cfg.PipeBufferBytes
	s.bufPool.New = func() any { b := make([]byte, bufSize); return &b }
	return s, nil
}

// ID returns the relay id.
func (s *Server) ID() string { return s.cfg.ID }

// Start opens the listeners and federation links.
func (s *Server) Start() error {
	qcfg := s.cfg.QUIC
	if qcfg.CertHash == "" {
		qcfg.CertHash = s.cfg.CertHash
	}
	ep, err := netx.NewEndpoint(netx.Options{
		Mode:     s.cfg.Mode,
		QUIC:     qcfg,
		CertFile: s.cfg.CertFile,
		KeyFile:  s.cfg.KeyFile,
		Legacy:   s.handleLegacy,
		Logger:   s.log,
	})
	if err != nil {
		return err
	}
	host, portStr, err := net.SplitHostPort(s.cfg.Listen)
	if err != nil {
		ep.Close()
		return fmt.Errorf("relay: listen address %q: %w", s.cfg.Listen, err)
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	ln, err := ep.Listen(host, port)
	if err != nil {
		ep.Close()
		return err
	}
	s.ep, s.ln = ep, ln
	s.started = time.Now()

	if s.cfg.LegacyListen != "" {
		l, err := net.Listen("tcp", s.cfg.LegacyListen)
		if err != nil {
			s.log.Printf("[relay] legacy listener disabled: %v", err)
		} else {
			s.legacy = l
			s.wg.Add(1)
			go s.acceptLegacy()
		}
	}

	s.wg.Add(2)
	go s.acceptLoop()
	go s.housekeeping()

	for _, p := range s.cfg.Peers {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		s.wg.Add(1)
		go s.peerLoop(p)
	}
	s.log.Printf("[relay] %s listening on %s (quic=%v)", s.cfg.ID, s.Addr(), ep.QUICAvailable())
	return nil
}

// Addr returns the listening "host:port".
func (s *Server) Addr() string {
	host, _, _ := net.SplitHostPort(s.cfg.Listen)
	return net.JoinHostPort(host, fmt.Sprint(s.ln.Port()))
}

// Port returns the bound port.
func (s *Server) Port() int { return s.ln.Port() }

// QUICEnabled reports whether the relay accepts QUIC.
func (s *Server) QUICEnabled() bool { return s.ep != nil && s.ep.QUICAvailable() }

// Close stops the relay and disconnects everyone.
func (s *Server) Close() error {
	s.cancel()
	if s.ln != nil {
		s.ln.Close()
	}
	if s.legacy != nil {
		s.legacy.Close()
	}
	s.mu.RLock()
	all := make([]*session, 0, len(s.sessions)+len(s.links))
	for _, ss := range s.sessions {
		all = append(all, ss)
	}
	for _, l := range s.links {
		all = append(all, l.sess)
	}
	s.mu.RUnlock()
	for _, ss := range all {
		ss.close("relay shutting down")
	}
	s.wg.Wait()
	if s.ep != nil {
		s.ep.Close()
	}
	return nil
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept(s.ctx)
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(c)
		}()
	}
}

func (s *Server) housekeeping() {
	defer s.wg.Done()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	linkPing := time.NewTicker(10 * time.Second)
	defer linkPing.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.offline.sweep()
		case <-linkPing.C:
			s.mu.RLock()
			for _, l := range s.links {
				l.sess.enqueue(wire.EncodePing(0))
			}
			s.mu.RUnlock()
		}
	}
}

// ===== sessions =====

type session struct {
	srv   *Server
	id    string
	keyFP string // identity key fingerprint ("" = node without a key)
	conn  netx.Conn
	ctrl  netx.Stream
	w     *wire.QueueWriter
	info  wire.NodeInfo
	link  *link // non-nil for relay links

	watch   atomic.Bool
	limiter *tokenBucket

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	closed    chan struct{}

	subsMu sync.Mutex
	subs   map[string]string // pattern -> queue group

	in      atomic.Uint64
	out     atomic.Uint64
	dropped atomic.Uint64
	since   time.Time
}

func (s *Server) newSession(c netx.Conn, ctrl netx.Stream, id string, queueBytes int64) *session {
	ctx, cancel := context.WithCancel(s.ctx)
	ss := &session{
		srv:    s,
		id:     id,
		conn:   c,
		ctrl:   ctrl,
		w:      wire.NewQueueWriterPaused(ctrl, queueBytes), // started after registration
		ctx:    ctx,
		cancel: cancel,
		closed: make(chan struct{}),
		subs:   make(map[string]string),
		since:  time.Now(),
	}
	if s.cfg.RateLimit > 0 {
		ss.limiter = newTokenBucket(s.cfg.RateLimit, float64(s.cfg.RateBurst))
	}
	return ss
}

// enqueue delivers a frame to the node, applying the slow-consumer policy.
func (ss *session) enqueue(frame []byte) bool {
	err := ss.w.TrySend(frame)
	if err == nil {
		ss.out.Add(1)
		return true
	}
	if err == wire.ErrQueueFull {
		ss.dropped.Add(1)
		ss.srv.st.dropped.Add(1)
		if ss.srv.cfg.SlowConsumer == SlowDisconnect && ss.link == nil {
			go ss.close("slow consumer")
		}
	}
	return false
}

func (ss *session) sendError(code uint16, ref uint64, msg string) {
	e := wire.Error{Code: code, Ref: ref, Msg: msg}
	ss.w.TrySend(e.Encode())
}

func (ss *session) close(reason string) {
	ss.closeOnce.Do(func() {
		ss.srv.log.Printf("[relay] %s %s disconnected: %s", kindOf(ss), ss.id, reason)
		ss.cancel()
		ss.ctrl.SetWriteDeadline(time.Now().Add(2 * time.Second)) // never hang on a stalled peer
		ss.w.Close()
		ss.conn.Close()
		if ss.link != nil {
			ss.srv.unregisterLink(ss.link)
		} else {
			ss.srv.unregisterSession(ss)
		}
		close(ss.closed)
	})
}

func kindOf(ss *session) string {
	if ss.link != nil {
		return "relay"
	}
	return "node"
}

func (s *Server) handleConn(c netx.Conn) {
	hsCtx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	ctrl, err := c.AcceptStream(hsCtx)
	cancel()
	if err != nil {
		c.Close()
		return
	}
	ctrl.SetReadDeadline(time.Now().Add(15 * time.Second))
	typ, body, err := wire.ReadFrameExact(ctrl, 64<<10)
	ctrl.SetReadDeadline(time.Time{})
	if err != nil || typ != wire.THello {
		ctrl.Close()
		c.Close()
		return
	}
	hello, err := wire.DecodeHello(body)
	if err != nil {
		reject(ctrl, c, wire.ErrCodeBadRequest, "malformed hello")
		return
	}
	if hello.Version != wire.Version {
		reject(ctrl, c, wire.ErrCodeVersion, fmt.Sprintf("protocol version %d not supported (want %d)", hello.Version, wire.Version))
		return
	}

	if hello.Flags&wire.HelloRelayLink != 0 {
		s.acceptLink(c, ctrl, hello)
		return
	}

	if len(s.cfg.AuthTokens) > 0 && !tokenOK(s.cfg.AuthTokens, hello.Token) {
		s.st.authFailures.Add(1)
		reject(ctrl, c, wire.ErrCodeUnauthorized, "invalid token")
		return
	}
	id := hello.NodeID
	if id == "" {
		id = "n-" + randomID(8)
	}
	if !validNodeID(id) {
		reject(ctrl, c, wire.ErrCodeBadRequest, "invalid node id (use A-Z a-z 0-9 . _ - : @, max 128)")
		return
	}
	keyFP, why := s.ids.check(hello, id, time.Now(), s.cfg.RequireIdentity)
	if why != "" {
		s.st.authFailures.Add(1)
		s.log.Printf("[relay] rejected %s from %s: %s", id, c.RemoteAddr(), why)
		reject(ctrl, c, wire.ErrCodeUnauthorized, why)
		return
	}

	s.mu.RLock()
	count := len(s.sessions)
	s.mu.RUnlock()
	if count >= s.cfg.MaxSessions {
		reject(ctrl, c, wire.ErrCodeOverloaded, "relay full")
		return
	}

	ss := s.newSession(c, ctrl, id, s.cfg.MaxQueueBytes)
	ss.keyFP = keyFP
	ss.info = wire.NodeInfo{NodeID: id, Addrs: limitStrings(hello.Addrs, 16), Observed: c.RemoteAddr(), Meta: hello.Meta, Relay: s.cfg.ID}
	ss.watch.Store(hello.Flags&wire.HelloWatchPresence != 0)

	welcome := wire.Welcome{
		Version:    wire.Version,
		NodeID:     id,
		RelayID:    s.cfg.ID,
		Observed:   c.RemoteAddr(),
		MaxFrame:   uint32(s.cfg.MaxFrame),
		ServerTime: uint64(time.Now().UnixMilli()),
	}
	// WELCOME is queued first, the session is registered, then the writer
	// starts: once a node has read WELCOME it is reachable by everyone.
	if err := ss.w.TrySend(welcome.Encode()); err != nil {
		ss.close("handshake write failed")
		return
	}
	s.registerSession(ss)
	ss.w.Start()
	s.st.connects.Add(1)
	s.log.Printf("[relay] node %s connected via %s from %s (key %s)", id, c.Protocol(), c.RemoteAddr(), orNone(keyFP))

	go s.acceptPipes(ss)
	go s.datagramLoop(ss)
	s.readLoop(ss)
}

func reject(ctrl netx.Stream, c netx.Conn, code uint16, msg string) {
	e := wire.Error{Code: code, Msg: msg}
	ctrl.SetWriteDeadline(time.Now().Add(2 * time.Second))
	ctrl.Write(e.Encode())
	ctrl.CloseWrite()
	time.Sleep(50 * time.Millisecond) // let the error reach the peer before closing
	ctrl.Close()
	c.Close()
}

func tokenOK(tokens []string, tok string) bool {
	ok := false
	for _, t := range tokens {
		if subtle.ConstantTimeCompare([]byte(t), []byte(tok)) == 1 {
			ok = true
		}
	}
	return ok
}

// registerSession makes a node reachable, replacing an older session with
// the same id (the usual case after a reconnect). The swap is atomic so the
// old session's cleanup cannot remove what belongs to the new one.
func (s *Server) registerSession(ss *session) {
	s.mu.Lock()
	old := s.sessions[ss.id]
	s.sessions[ss.id] = ss
	links := s.linkList()
	s.mu.Unlock()
	if old != nil {
		old.close("replaced by a new connection")
	}

	if links != nil {
		frame := wire.EncodeRouteAdd([]wire.NodeInfo{ss.info})
		for _, l := range links {
			l.sess.enqueue(frame)
		}
	}
	if old == nil {
		// A replaced session never announced a leave, so watchers still
		// consider the node present.
		s.broadcastPresence(wire.PresenceJoin, ss.id, ss.info.Meta, ss)
	}

	for _, f := range s.offline.take(ss.id) {
		ss.enqueue(f)
	}
}

// unregisterSession removes a closed session. Its subscriptions are always
// dropped; the node itself (route, files, presence) only when this session
// is still the current one, i.e. it was not replaced by a reconnect.
func (s *Server) unregisterSession(ss *session) {
	s.mu.Lock()
	current := s.sessions[ss.id] == ss
	if current {
		delete(s.sessions, ss.id)
	}
	links := s.linkList()
	s.mu.Unlock()

	gone := s.subs.removeAll(ss)
	var removedFiles []string
	if current {
		removedFiles = s.files.removeProvider(ss.id)
	}
	for _, l := range links {
		if current {
			l.sess.enqueue(wire.EncodeStrList(wire.TRouteDel, []string{ss.id}))
		}
		if len(gone) > 0 {
			l.sess.enqueue(wire.EncodeStrList(wire.TInterestDel, gone))
		}
		for _, h := range removedFiles {
			fu := wire.FileUnpublish{Provider: ss.id, Hash: h}
			l.sess.enqueue(fu.Encode())
		}
	}
	if current {
		s.broadcastPresence(wire.PresenceLeave, ss.id, "", nil)
	}
}

// linkList returns current links; caller holds s.mu.
func (s *Server) linkList() []*link {
	if len(s.links) == 0 {
		return nil
	}
	out := make([]*link, 0, len(s.links))
	for _, l := range s.links {
		out = append(out, l)
	}
	return out
}

func (s *Server) broadcastPresence(event uint8, nodeID, meta string, except *session) {
	p := wire.Presence{Event: event, NodeID: nodeID, Meta: meta}
	var frame []byte
	s.mu.RLock()
	for _, other := range s.sessions {
		if other == except || !other.watch.Load() {
			continue
		}
		if frame == nil {
			frame = p.Encode()
		}
		other.enqueue(frame)
	}
	s.mu.RUnlock()
}

func (s *Server) readLoop(ss *session) {
	defer ss.close("connection closed")
	r := newFrameReader(ss.ctrl)
	for {
		ss.ctrl.SetReadDeadline(time.Now().Add(s.cfg.SessionTimeout))
		typ, body, err := r.next(s.cfg.MaxFrame + 1024)
		if err != nil {
			if errors.Is(err, wire.ErrFrameTooLarge) {
				ss.sendError(wire.ErrCodeTooLarge, 0, err.Error())
			}
			return
		}
		ss.in.Add(1)
		if err := s.dispatch(ss, typ, body); err != nil {
			ss.sendError(wire.ErrCodeBadRequest, 0, err.Error())
		}
	}
}

// ===== helpers =====

func randomID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func validNodeID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-' || c == ':' || c == '@':
		default:
			return false
		}
	}
	return true
}

func limitStrings(ss []string, n int) []string {
	if len(ss) > n {
		return ss[:n]
	}
	return ss
}

// tokenBucket is a small rate limiter.
type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
	rate   float64
	burst  float64
}

func newTokenBucket(rate, burst float64) *tokenBucket {
	return &tokenBucket{tokens: burst, last: time.Now(), rate: rate, burst: burst}
}

func (b *tokenBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	b.last = now
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
