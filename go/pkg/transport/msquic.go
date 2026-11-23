//go:build cgo && !nomsquic

package transport

/*
#cgo CFLAGS: -O2 -I${SRCDIR}/../../../msquic/src/inc -DMAGNETK_STATIC
#cgo windows LDFLAGS: -L${SRCDIR}/../../../msquic/artifacts/bin/windows/x64_Release_schannel -lmsquic -lws2_32 -lcrypt32 -lncrypt
#cgo linux LDFLAGS: -L${SRCDIR}/../../../msquic/artifacts/bin/linux/x64_Release_openssl -L${SRCDIR}/../../../msquic/artifacts/bin/linux/x64_Release_quictls -L${SRCDIR}/../../../msquic/build/bin/Release -lmsquic -lpthread -Wl,-rpath,$ORIGIN
#cgo darwin LDFLAGS: -L${SRCDIR}/../../../msquic/artifacts/bin/macos/x64_Release_openssl -L${SRCDIR}/../../../msquic/build/bin/Release -lmsquic -lpthread

#include <stdlib.h>
#include "transport.h"

extern void goMagnetkNotify(uint64_t token, uint32_t events);
static void mk_install_notify(magnetk_transport_t* t) {
    magnetk_transport_set_notify(t, (magnetk_notify_fn)goMagnetkNotify);
}
*/
import "C"

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// Available reports whether MsQuic is compiled in.
func Available() bool { return true }

const maxWriteChunk = 1 << 20

// ===== notification registry =====

type notifier interface{ notify(ev uint32) }

var (
	registry  sync.Map // uint64 -> notifier
	nextToken atomic.Uint64
)

func register(n notifier) uint64 {
	tok := nextToken.Add(1)
	registry.Store(tok, n)
	return tok
}

func unregister(tok uint64) { registry.Delete(tok) }

//export goMagnetkNotify
func goMagnetkNotify(token C.uint64_t, events C.uint32_t) {
	if v, ok := registry.Load(uint64(token)); ok {
		v.(notifier).notify(uint32(events))
	}
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func statusErr(st C.magnetk_status_t) error {
	switch st {
	case C.MAGNETK_OK:
		return nil
	case C.MAGNETK_ERR_TIMEOUT:
		return ErrTimeout
	case C.MAGNETK_ERR_CLOSED:
		return ErrClosed
	case C.MAGNETK_ERR_RESET:
		return ErrReset
	case C.MAGNETK_ERR_TLS:
		return ErrTLS
	case C.MAGNETK_ERR_CONNECT:
		return ErrConnect
	case C.MAGNETK_ERR_TOO_LARGE:
		return ErrDatagramTooLarge
	default:
		return fmt.Errorf("transport: %s", C.GoString(C.magnetk_status_string(st)))
	}
}

// waitFor blocks until ch fires, the deadline passes, ctx ends or done closes.
func waitFor(ch, done <-chan struct{}, deadline time.Time, ctx context.Context) error {
	var timer <-chan time.Time
	if !deadline.IsZero() {
		d := time.Until(deadline)
		if d <= 0 {
			return os.ErrDeadlineExceeded
		}
		t := time.NewTimer(d)
		defer t.Stop()
		timer = t.C
	}
	var ctxDone <-chan struct{}
	if ctx != nil {
		ctxDone = ctx.Done()
	}
	select {
	case <-ch:
		return nil
	case <-timer:
		return os.ErrDeadlineExceeded
	case <-ctxDone:
		return ctx.Err()
	case <-done:
		return ErrClosed
	}
}

// ===== Transport =====

// Transport owns one MsQuic registration plus every object created from it.
type Transport struct {
	h   *C.magnetk_transport_t
	cfg Config

	mu        sync.Mutex
	closed    bool
	listeners map[*Listener]struct{}
	conns     map[*Conn]struct{}
}

// New creates a transport.
func New(cfg Config) (*Transport, error) {
	cfg = cfg.withDefaults()

	var cc C.magnetk_config_t
	C.magnetk_config_default(&cc)

	strs := []*C.char{}
	cstr := func(s string) *C.char {
		if s == "" {
			return nil
		}
		p := C.CString(s)
		strs = append(strs, p)
		return p
	}
	defer func() {
		for _, p := range strs {
			C.free(unsafe.Pointer(p))
		}
	}()

	cc.bind_address = cstr(cfg.BindAddress)
	cc.port = C.uint16_t(cfg.Port)
	cc.cert_file = cstr(cfg.CertFile)
	cc.key_file = cstr(cfg.KeyFile)
	cc.cert_hash = cstr(cfg.CertHash)
	cc.cert_machine_store = boolInt(cfg.CertMachineStore)
	cc.no_cert_validation = boolInt(cfg.NoCertValidation)
	cc.alpn = cstr(cfg.ALPN)
	cc.max_connections = C.uint32_t(cfg.MaxConnections)
	cc.max_streams_per_conn = C.uint16_t(cfg.MaxStreamsPerConn)
	cc.idle_timeout_ms = C.uint32_t(cfg.IdleTimeout / time.Millisecond)
	cc.handshake_timeout_ms = C.uint32_t(cfg.HandshakeTimeout / time.Millisecond)
	cc.keepalive_ms = C.uint32_t(cfg.KeepAlive / time.Millisecond)
	cc.send_buffer_size = C.uint32_t(cfg.SendWindow)
	cc.recv_buffer_size = C.uint32_t(cfg.RecvWindow)
	cc.conn_flow_window = C.uint32_t(cfg.ConnWindow)
	cc.execution_profile = C.int(cfg.Profile.cValue())
	cc.congestion_control = C.int(cfg.Congestion.cValue())
	cc.enable_datagrams = boolInt(!cfg.DisableDatagrams)

	var h *C.magnetk_transport_t
	if err := statusErr(C.magnetk_transport_init(&cc, &h)); err != nil {
		return nil, fmt.Errorf("transport init: %w", err)
	}
	C.mk_install_notify(h)
	return &Transport{
		h:         h,
		cfg:       cfg,
		listeners: make(map[*Listener]struct{}),
		conns:     make(map[*Conn]struct{}),
	}, nil
}

func boolInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// Config returns the effective configuration.
func (t *Transport) Config() Config { return t.cfg }

// Close closes every listener, connection and stream, then frees MsQuic.
func (t *Transport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	ls := make([]*Listener, 0, len(t.listeners))
	for l := range t.listeners {
		ls = append(ls, l)
	}
	cs := make([]*Conn, 0, len(t.conns))
	for c := range t.conns {
		cs = append(cs, c)
	}
	t.mu.Unlock()

	for _, l := range ls {
		l.Close()
	}
	for _, c := range cs {
		c.Close()
	}
	C.magnetk_transport_shutdown(t.h)
	return nil
}

func (t *Transport) track(c *Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	t.conns[c] = struct{}{}
	return true
}

func (t *Transport) untrack(c *Conn) {
	t.mu.Lock()
	delete(t.conns, c)
	t.mu.Unlock()
}

// ===== Listener =====

// Listener accepts inbound connections.
type Listener struct {
	t      *Transport
	h      *C.magnetk_listener_t
	token  uint64
	ready  chan struct{}
	done   chan struct{}
	mu     sync.RWMutex
	closed bool
	once   sync.Once
}

func (l *Listener) notify(ev uint32) {
	if ev&C.MAGNETK_EV_ACCEPT != 0 {
		signal(l.ready)
	}
}

// Listen starts listening on cfg.BindAddress:cfg.Port.
func (t *Transport) Listen() (*Listener, error) {
	return t.ListenOn(t.cfg.BindAddress, t.cfg.Port)
}

// ListenOn starts listening on bind:port (port 0 = ephemeral). A transport
// can run several listeners alongside outgoing connections.
func (t *Transport) ListenOn(bind string, port uint16) (*Listener, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, ErrClosed
	}
	t.mu.Unlock()

	cbind := C.CString(bind)
	defer C.free(unsafe.Pointer(cbind))
	var h *C.magnetk_listener_t
	if err := statusErr(C.magnetk_listen_on(t.h, cbind, C.uint16_t(port), &h)); err != nil {
		return nil, fmt.Errorf("quic listen on %s:%d: %w", bind, port, err)
	}
	l := &Listener{t: t, h: h, ready: make(chan struct{}, 1), done: make(chan struct{})}
	l.token = register(l)
	C.magnetk_listener_set_token(h, C.uint64_t(l.token))

	t.mu.Lock()
	t.listeners[l] = struct{}{}
	t.mu.Unlock()
	return l, nil
}

// Port returns the bound UDP port.
func (l *Listener) Port() uint16 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return 0
	}
	return uint16(C.magnetk_listener_port(l.h))
}

// Accept waits for a connection whose handshake has completed.
func (l *Listener) Accept(ctx context.Context) (*Conn, error) {
	for {
		l.mu.RLock()
		if l.closed {
			l.mu.RUnlock()
			return nil, ErrClosed
		}
		var ch *C.magnetk_connection_t
		st := C.magnetk_listener_accept(l.h, 0, &ch)
		l.mu.RUnlock()
		switch st {
		case C.MAGNETK_OK:
			c := newConn(l.t, ch)
			if c == nil {
				return nil, ErrClosed
			}
			return c, nil
		case C.MAGNETK_WOULD_BLOCK:
			if err := waitFor(l.ready, l.done, time.Time{}, ctx); err != nil {
				return nil, err
			}
		default:
			return nil, statusErr(st)
		}
	}
}

// Close stops accepting; established connections are unaffected.
func (l *Listener) Close() error {
	l.once.Do(func() {
		close(l.done)
		l.mu.Lock()
		l.closed = true
		unregister(l.token)
		C.magnetk_listener_close(l.h)
		l.mu.Unlock()
		l.t.mu.Lock()
		delete(l.t.listeners, l)
		l.t.mu.Unlock()
	})
	return nil
}

// ===== Connection =====

// Conn is a QUIC connection. Both sides may open and accept streams.
type Conn struct {
	t      *Transport
	h      *C.magnetk_connection_t
	token  uint64
	remote string

	acceptCh chan struct{}
	dgramCh  chan struct{}
	dead     chan struct{} // closed when the peer/transport shut the connection
	deadOnce sync.Once
	done     chan struct{} // closed by Close
	once     sync.Once

	mu      sync.RWMutex
	closed  bool
	streams map[*Stream]struct{}
}

func newConn(t *Transport, h *C.magnetk_connection_t) *Conn {
	c := &Conn{
		t:        t,
		h:        h,
		acceptCh: make(chan struct{}, 1),
		dgramCh:  make(chan struct{}, 1),
		dead:     make(chan struct{}),
		done:     make(chan struct{}),
		streams:  make(map[*Stream]struct{}),
	}
	var buf [96]C.char
	n := C.magnetk_connection_remote_addr(h, &buf[0], C.int(len(buf)))
	c.remote = C.GoStringN(&buf[0], n)
	if !t.track(c) {
		C.magnetk_connection_close(h)
		return nil
	}
	c.token = register(c)
	C.magnetk_connection_set_token(h, C.uint64_t(c.token))
	return c
}

func (c *Conn) notify(ev uint32) {
	if ev&C.MAGNETK_EV_ACCEPT != 0 {
		signal(c.acceptCh)
	}
	if ev&C.MAGNETK_EV_DATAGRAM != 0 {
		signal(c.dgramCh)
	}
	if ev&C.MAGNETK_EV_CLOSED != 0 {
		c.deadOnce.Do(func() { close(c.dead) })
		// Wake goroutines parked in AcceptStream/ReceiveDatagram so they
		// observe the closed state instead of waiting for Close().
		signal(c.acceptCh)
		signal(c.dgramCh)
	}
}

// Dial connects to host:port and completes the TLS handshake.
func (t *Transport) Dial(ctx context.Context, host string, port uint16) (*Conn, error) {
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	timeout := t.cfg.HandshakeTimeout
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d < timeout {
			timeout = d
		}
	}
	if timeout <= 0 {
		return nil, ErrTimeout
	}
	chost := C.CString(host)
	defer C.free(unsafe.Pointer(chost))
	var h *C.magnetk_connection_t
	st := C.magnetk_connect(t.h, chost, C.uint16_t(port), C.uint32_t(timeout/time.Millisecond), &h)
	if err := statusErr(st); err != nil {
		return nil, fmt.Errorf("quic dial %s:%d: %w", host, port, err)
	}
	c := newConn(t, h)
	if c == nil {
		return nil, ErrClosed
	}
	return c, nil
}

// RemoteAddr returns "ip:port" of the peer.
func (c *Conn) RemoteAddr() string { return c.remote }

// Done is closed once the connection has shut down for any reason.
func (c *Conn) Done() <-chan struct{} { return c.dead }

// Alive reports whether the connection is still usable.
func (c *Conn) Alive() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.closed && C.magnetk_connection_is_alive(c.h) != 0
}

func (c *Conn) addStream(s *Stream) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.streams[s] = struct{}{}
	return true
}

func (c *Conn) removeStream(s *Stream) {
	c.mu.Lock()
	delete(c.streams, s)
	c.mu.Unlock()
}

// OpenStream opens a bidirectional stream (the peer is told immediately).
func (c *Conn) OpenStream(ctx context.Context) (*Stream, error) {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return nil, ErrClosed
	}
	var sh *C.magnetk_stream_t
	st := C.magnetk_stream_open(c.h, &sh)
	c.mu.RUnlock()
	if err := statusErr(st); err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	return newStream(c, sh)
}

// AcceptStream waits for a stream opened by the peer.
func (c *Conn) AcceptStream(ctx context.Context) (*Stream, error) {
	for {
		c.mu.RLock()
		if c.closed {
			c.mu.RUnlock()
			return nil, ErrClosed
		}
		var sh *C.magnetk_stream_t
		st := C.magnetk_connection_accept_stream(c.h, 0, &sh)
		c.mu.RUnlock()
		switch st {
		case C.MAGNETK_OK:
			return newStream(c, sh)
		case C.MAGNETK_WOULD_BLOCK:
			if err := waitFor(c.acceptCh, c.done, time.Time{}, ctx); err != nil {
				return nil, err
			}
		default:
			return nil, statusErr(st)
		}
	}
}

// MaxDatagram is the largest datagram currently allowed (0 = unavailable).
func (c *Conn) MaxDatagram() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return 0
	}
	return int(C.magnetk_connection_max_datagram(c.h))
}

// SendDatagram sends an unreliable, unordered datagram.
func (c *Conn) SendDatagram(b []byte) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return ErrClosed
	}
	var p *C.uint8_t
	if len(b) > 0 {
		p = (*C.uint8_t)(unsafe.Pointer(&b[0]))
	}
	st := C.magnetk_connection_send_datagram(c.h, p, C.uint32_t(len(b)))
	if st == C.MAGNETK_ERR_NOT_READY {
		return ErrDatagramUnavailable
	}
	return statusErr(st)
}

// ReceiveDatagram waits for the next datagram.
func (c *Conn) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	buf := make([]byte, 2048)
	for {
		c.mu.RLock()
		if c.closed {
			c.mu.RUnlock()
			return nil, ErrClosed
		}
		var n C.uint32_t
		st := C.magnetk_connection_recv_datagram(c.h, (*C.uint8_t)(unsafe.Pointer(&buf[0])), C.uint32_t(len(buf)), &n, 0)
		c.mu.RUnlock()
		switch st {
		case C.MAGNETK_OK:
			return buf[:n], nil
		case C.MAGNETK_WOULD_BLOCK:
			if err := waitFor(c.dgramCh, c.done, time.Time{}, ctx); err != nil {
				return nil, err
			}
		default:
			return nil, statusErr(st)
		}
	}
}

// Stats returns MsQuic connection statistics.
func (c *Conn) Stats() ConnStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return ConnStats{}
	}
	var s C.magnetk_conn_stats_t
	if C.magnetk_connection_stats(c.h, &s) != C.MAGNETK_OK {
		return ConnStats{}
	}
	return ConnStats{
		RTT:              time.Duration(s.rtt_us) * time.Microsecond,
		MinRTT:           time.Duration(s.min_rtt_us) * time.Microsecond,
		CongestionWindow: uint32(s.congestion_window),
		PathMTU:          uint32(s.path_mtu),
		SentBytes:        uint64(s.send_bytes),
		RecvBytes:        uint64(s.recv_bytes),
		SentPackets:      uint64(s.send_packets),
		LostPackets:      uint64(s.lost_packets),
	}
}

// CloseWithError shuts the connection down, sending code to the peer.
func (c *Conn) CloseWithError(code uint64) error {
	c.mu.RLock()
	if !c.closed {
		C.magnetk_connection_shutdown(c.h, C.uint64_t(code))
	}
	c.mu.RUnlock()
	return c.Close()
}

// Close closes all streams of this connection and the connection itself.
func (c *Conn) Close() error {
	c.once.Do(func() {
		close(c.done)
		c.mu.Lock()
		c.closed = true
		streams := make([]*Stream, 0, len(c.streams))
		for s := range c.streams {
			streams = append(streams, s)
		}
		c.mu.Unlock()
		for _, s := range streams {
			s.Close()
		}
		c.mu.Lock()
		unregister(c.token)
		C.magnetk_connection_close(c.h)
		c.mu.Unlock()
		c.deadOnce.Do(func() { close(c.dead) })
		c.t.untrack(c)
	})
	return nil
}

// ===== Stream =====

// Stream is a bidirectional QUIC stream. It implements io.ReadWriteCloser and
// the deadline methods of net.Conn. Reads and writes may run concurrently
// with each other, but not with themselves.
type Stream struct {
	c     *Conn
	h     *C.magnetk_stream_t
	token uint64
	id    uint64

	readCh  chan struct{}
	writeCh chan struct{}
	done    chan struct{}
	once    sync.Once

	mu     sync.RWMutex
	closed bool

	readDeadline  atomic.Pointer[time.Time]
	writeDeadline atomic.Pointer[time.Time]
}

func newStream(c *Conn, h *C.magnetk_stream_t) (*Stream, error) {
	s := &Stream{
		c:       c,
		h:       h,
		readCh:  make(chan struct{}, 1),
		writeCh: make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	if !c.addStream(s) {
		C.magnetk_stream_close(h)
		return nil, ErrClosed
	}
	s.token = register(s)
	C.magnetk_stream_set_token(h, C.uint64_t(s.token))
	return s, nil
}

func (s *Stream) notify(ev uint32) {
	if ev&(C.MAGNETK_EV_READABLE|C.MAGNETK_EV_CLOSED) != 0 {
		signal(s.readCh)
	}
	if ev&(C.MAGNETK_EV_WRITABLE|C.MAGNETK_EV_CLOSED) != 0 {
		signal(s.writeCh)
	}
}

// ID returns the QUIC stream id (0 until the stream has started).
func (s *Stream) ID() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return 0
	}
	return uint64(C.magnetk_stream_id(s.h))
}

// Conn returns the connection the stream belongs to.
func (s *Stream) Conn() *Conn { return s.c }

func deadlineOf(p *atomic.Pointer[time.Time]) time.Time {
	if t := p.Load(); t != nil {
		return *t
	}
	return time.Time{}
}

// Read reads data; io.EOF is returned once the peer finished sending.
func (s *Stream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(p) > 1<<30 {
		p = p[:1<<30]
	}
	for {
		s.mu.RLock()
		if s.closed {
			s.mu.RUnlock()
			return 0, ErrClosed
		}
		var n C.uint32_t
		st := C.magnetk_stream_recv(s.h, (*C.uint8_t)(unsafe.Pointer(&p[0])), C.uint32_t(len(p)), &n, 0)
		s.mu.RUnlock()
		switch st {
		case C.MAGNETK_OK:
			return int(n), nil
		case C.MAGNETK_ERR_CLOSED:
			return 0, io.EOF
		case C.MAGNETK_ERR_RESET:
			return 0, ErrReset
		case C.MAGNETK_WOULD_BLOCK:
			if err := waitFor(s.readCh, s.done, deadlineOf(&s.readDeadline), nil); err != nil {
				return 0, err
			}
		default:
			return 0, statusErr(st)
		}
	}
}

// Write queues p for sending. It blocks while the send window is full.
func (s *Stream) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		chunk := p[written:]
		if len(chunk) > maxWriteChunk {
			chunk = chunk[:maxWriteChunk]
		}
		s.mu.RLock()
		if s.closed {
			s.mu.RUnlock()
			return written, ErrClosed
		}
		st := C.magnetk_stream_send(s.h, (*C.uint8_t)(unsafe.Pointer(&chunk[0])), C.uint32_t(len(chunk)), 0, 0)
		s.mu.RUnlock()
		switch st {
		case C.MAGNETK_OK:
			written += len(chunk)
		case C.MAGNETK_WOULD_BLOCK:
			if err := waitFor(s.writeCh, s.done, deadlineOf(&s.writeDeadline), nil); err != nil {
				return written, err
			}
		case C.MAGNETK_ERR_CLOSED:
			return written, ErrClosed
		default:
			return written, fmt.Errorf("stream write: %w", statusErr(st))
		}
	}
	return written, nil
}

// Flush waits until every written byte has been acknowledged by the peer.
func (s *Stream) Flush(ctx context.Context) error {
	for {
		s.mu.RLock()
		if s.closed {
			s.mu.RUnlock()
			return ErrClosed
		}
		st := C.magnetk_stream_flush(s.h, 0)
		s.mu.RUnlock()
		switch st {
		case C.MAGNETK_OK:
			return nil
		case C.MAGNETK_WOULD_BLOCK:
			if err := waitFor(s.writeCh, s.done, time.Time{}, ctx); err != nil {
				return err
			}
		default:
			return statusErr(st)
		}
	}
}

// CloseWrite sends FIN after all queued data; reading continues to work.
func (s *Stream) CloseWrite() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrClosed
	}
	return statusErr(C.magnetk_stream_shutdown_send(s.h))
}

// Abort resets both directions immediately.
func (s *Stream) Abort(code uint64) {
	s.mu.RLock()
	if !s.closed {
		C.magnetk_stream_abort(s.h, C.uint64_t(code))
	}
	s.mu.RUnlock()
	s.Close()
}

// Close releases the stream. Queued data is still delivered (FIN is sent),
// unread incoming data is discarded.
func (s *Stream) Close() error {
	s.once.Do(func() {
		close(s.done)
		s.mu.Lock()
		s.closed = true
		unregister(s.token)
		C.magnetk_stream_close(s.h)
		s.mu.Unlock()
		s.c.removeStream(s)
	})
	return nil
}

// SetDeadline sets both read and write deadlines.
func (s *Stream) SetDeadline(t time.Time) error {
	s.SetReadDeadline(t)
	return s.SetWriteDeadline(t)
}

// SetReadDeadline sets the read deadline (zero = none).
func (s *Stream) SetReadDeadline(t time.Time) error {
	s.readDeadline.Store(&t)
	signal(s.readCh) // re-evaluate a pending wait
	return nil
}

// SetWriteDeadline sets the write deadline (zero = none).
func (s *Stream) SetWriteDeadline(t time.Time) error {
	s.writeDeadline.Store(&t)
	signal(s.writeCh)
	return nil
}
