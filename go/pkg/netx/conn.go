package netx

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-yamux/v4"

	"github.com/iMarand/magnetk/go/pkg/transport"
)

// ===== QUIC =====

type quicConn struct {
	c *transport.Conn
}

func (q *quicConn) OpenStream(ctx context.Context) (Stream, error) {
	s, err := q.c.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (q *quicConn) AcceptStream(ctx context.Context) (Stream, error) {
	s, err := q.c.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (q *quicConn) SendDatagram(b []byte) error { return q.c.SendDatagram(b) }
func (q *quicConn) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	return q.c.ReceiveDatagram(ctx)
}
func (q *quicConn) MaxDatagram() int           { return q.c.MaxDatagram() }
func (q *quicConn) RemoteAddr() string         { return q.c.RemoteAddr() }
func (q *quicConn) Protocol() string           { return "quic" }
func (q *quicConn) RTT() time.Duration         { return q.c.Stats().RTT }
func (q *quicConn) Done() <-chan struct{}      { return q.c.Done() }
func (q *quicConn) Close() error               { return q.c.Close() }
func (q *quicConn) Stats() transport.ConnStats { return q.c.Stats() }

// ===== TCP + TLS + yamux =====

type tcpConn struct {
	sess   *yamux.Session
	tls    *tls.Conn
	remote string
	rtt    atomic.Int64
	once   sync.Once
}

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.AcceptBacklog = 1024
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 15 * time.Second
	cfg.ConnectionWriteTimeout = 30 * time.Second
	cfg.MaxIncomingStreams = 4096
	cfg.MaxStreamWindowSize = 16 << 20
	cfg.ReadBufSize = 64 << 10
	cfg.MaxMessageSize = 256 << 10
	cfg.LogOutput = io.Discard
	return cfg
}

func newTCPConn(tc *tls.Conn, server bool) (*tcpConn, error) {
	var (
		sess *yamux.Session
		err  error
	)
	if server {
		sess, err = yamux.Server(tc, yamuxConfig(), nil)
	} else {
		sess, err = yamux.Client(tc, yamuxConfig(), nil)
	}
	if err != nil {
		return nil, err
	}
	c := &tcpConn{sess: sess, tls: tc, remote: tc.RemoteAddr().String()}
	go func() {
		if d, err := sess.Ping(); err == nil {
			c.rtt.Store(int64(d))
		}
	}()
	return c, nil
}

type tcpStream struct{ *yamux.Stream }

func (t *tcpConn) OpenStream(ctx context.Context) (Stream, error) {
	s, err := t.sess.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	return tcpStream{s}, nil
}

func (t *tcpConn) AcceptStream(ctx context.Context) (Stream, error) {
	type res struct {
		s   *yamux.Stream
		err error
	}
	if ctx.Done() == nil {
		s, err := t.sess.AcceptStream()
		if err != nil {
			return nil, err
		}
		return tcpStream{s}, nil
	}
	ch := make(chan res, 1)
	go func() {
		s, err := t.sess.AcceptStream()
		ch <- res{s, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return tcpStream{r.s}, nil
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.s != nil {
				r.s.Reset()
			}
		}()
		return nil, ctx.Err()
	}
}

func (t *tcpConn) SendDatagram([]byte) error { return ErrNoDatagrams }
func (t *tcpConn) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.sess.CloseChan():
		return nil, ErrClosed
	}
}
func (t *tcpConn) MaxDatagram() int      { return 0 }
func (t *tcpConn) RemoteAddr() string    { return t.remote }
func (t *tcpConn) Protocol() string      { return "tcp" }
func (t *tcpConn) RTT() time.Duration    { return time.Duration(t.rtt.Load()) }
func (t *tcpConn) Done() <-chan struct{} { return t.sess.CloseChan() }
func (t *tcpConn) Close() error {
	t.once.Do(func() { t.sess.Close() })
	return nil
}

// SecurityInfo describes the encryption of a connection.
type SecurityInfo struct {
	Version  string // e.g. "TLS 1.3"
	Cipher   string // e.g. "TLS_AES_128_GCM_SHA256" ("" when the transport does not expose it)
	PeerCert string // SHA-256 fingerprint (hex) of the peer certificate, if known
	ALPN     string
}

// Security returns the encryption details of c.
func Security(c Conn) SecurityInfo {
	switch t := c.(type) {
	case *tcpConn:
		st := t.tls.ConnectionState()
		si := SecurityInfo{Version: tls.VersionName(st.Version), Cipher: tls.CipherSuiteName(st.CipherSuite), ALPN: st.NegotiatedProtocol}
		if len(st.PeerCertificates) > 0 {
			sum := sha256.Sum256(st.PeerCertificates[0].Raw)
			si.PeerCert = hex.EncodeToString(sum[:])
		}
		return si
	case *quicConn:
		// QUIC always runs TLS 1.3 (RFC 9001); MsQuic negotiates the AEAD.
		return SecurityInfo{Version: "TLS 1.3 (QUIC)"}
	}
	return SecurityInfo{}
}
