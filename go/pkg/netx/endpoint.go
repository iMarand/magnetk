package netx

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/iMarand/magnetk/go/pkg/transport"
)

// Options configures an Endpoint.
type Options struct {
	Mode Mode

	// QUIC settings (bind address/port are taken from Listen).
	QUIC transport.Config

	// TLS material. PEM files are used by TCP everywhere and by QUIC on
	// Linux/macOS. When empty a self-signed certificate is generated.
	CertFile string
	KeyFile  string
	// Verify server certificates (production with real certificates).
	VerifyPeer bool
	// Optional CA bundle (PEM) used when VerifyPeer is set.
	CAFile string

	ALPN string

	// DialTimeout bounds one Dial attempt (default 10s).
	DialTimeout time.Duration
	// TCPFallbackDelay is how long auto mode waits for QUIC before also
	// trying TCP (default 400ms). The first transport to connect wins.
	TCPFallbackDelay time.Duration

	// Legacy receives raw TCP connections that do not start with TLS
	// (Magnetk v1 framing). nil = close them.
	Legacy func(net.Conn)

	Logger *log.Logger
}

// Endpoint creates listeners and dials connections.
type Endpoint struct {
	opts      Options
	quic      *transport.Transport // nil when QUIC is unavailable or disabled
	quicErr   error
	tlsServer *tls.Config
	tlsClient *tls.Config
	logger    *log.Logger

	tempFiles []string // generated PEM files for MsQuic, removed on Close

	mu        sync.Mutex
	closed    bool
	listeners []*multiListener
}

// NewEndpoint prepares TLS material and (unless Mode is tcp) a QUIC transport.
// When QUIC cannot be initialised the endpoint silently falls back to TCP in
// auto mode and fails in quic mode.
func NewEndpoint(opts Options) (*Endpoint, error) {
	if opts.Mode == "" {
		opts.Mode = ModeAuto
	}
	if opts.ALPN == "" {
		opts.ALPN = "magnetk/2"
	}
	if opts.DialTimeout == 0 {
		opts.DialTimeout = 10 * time.Second
	}
	if opts.TCPFallbackDelay == 0 {
		opts.TCPFallbackDelay = 400 * time.Millisecond
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	e := &Endpoint{opts: opts, logger: logger}

	cert, certPEM, keyPEM, err := loadOrGenerateCert(opts.CertFile, opts.KeyFile)
	if err != nil {
		return nil, err
	}
	e.tlsServer = &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{opts.ALPN},
		MinVersion:   tls.VersionTLS13,
	}
	e.tlsClient = &tls.Config{
		NextProtos:         []string{opts.ALPN},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: !opts.VerifyPeer, //nolint:gosec // dev mode is explicit in config
	}
	if opts.VerifyPeer && opts.CAFile != "" {
		pool := x509.NewCertPool()
		data, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool.AppendCertsFromPEM(data)
		e.tlsClient.RootCAs = pool
	}

	if opts.Mode != ModeTCP && !transport.Available() {
		e.quicErr = errors.New("this build has no QUIC transport (built without msquic)")
		if opts.Mode == ModeQUIC {
			return nil, e.quicErr
		}
	} else if opts.Mode != ModeTCP {
		qcfg := opts.QUIC
		if qcfg.ALPN == "" {
			qcfg.ALPN = opts.ALPN
		}
		qcfg.NoCertValidation = !opts.VerifyPeer
		if runtime.GOOS != "windows" && qcfg.CertFile == "" {
			if opts.CertFile != "" {
				qcfg.CertFile, qcfg.KeyFile = opts.CertFile, opts.KeyFile
			} else if qcfg.CertFile, qcfg.KeyFile, err = writeTempCert(certPEM, keyPEM); err == nil {
				e.tempFiles = []string{qcfg.CertFile, qcfg.KeyFile}
			} else {
				e.quicErr = fmt.Errorf("store QUIC certificate: %w", err)
			}
		}
		if e.quicErr == nil {
			e.quic, e.quicErr = transport.New(qcfg)
		}
		if e.quicErr != nil {
			if opts.Mode == ModeQUIC {
				return nil, e.quicErr
			}
			logger.Printf("[netx] QUIC unavailable (%v); using TCP only", e.quicErr)
		}
	}
	return e, nil
}

// QUICAvailable reports whether this endpoint can use QUIC.
func (e *Endpoint) QUICAvailable() bool { return e.quic != nil }

// Mode returns the configured mode.
func (e *Endpoint) Mode() Mode { return e.opts.Mode }

// Close closes listeners and the QUIC transport (and all its connections).
func (e *Endpoint) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	ls := e.listeners
	e.mu.Unlock()
	for _, l := range ls {
		l.Close()
	}
	if e.quic != nil {
		e.quic.Close()
	}
	for _, f := range e.tempFiles {
		os.Remove(f)
	}
	return nil
}

// ===== Listen =====

type multiListener struct {
	e       *Endpoint
	port    int
	quicL   *transport.Listener
	tcpL    net.Listener
	conns   chan Conn
	done    chan struct{}
	once    sync.Once
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	backlog int
}

// Listen serves QUIC (UDP) and TCP on bind:port. port 0 picks a free port
// that is used for both.
func (e *Endpoint) Listen(bind string, port int) (Listener, error) {
	if bind == "" {
		bind = "0.0.0.0"
	}
	ctx, cancel := context.WithCancel(context.Background())
	ml := &multiListener{e: e, conns: make(chan Conn, 256), done: make(chan struct{}), ctx: ctx, cancel: cancel}

	// With port 0 the UDP port is chosen first (by MsQuic) and TCP takes the
	// same number: on Windows, Hyper-V reserves UDP ranges inside the
	// ephemeral range, so picking the TCP port first keeps colliding.
	wantTCP := e.opts.Mode != ModeQUIC
	useQUIC := e.quic != nil
	var lastErr error
	for attempt := 0; attempt < 16; attempt++ {
		ml.tcpL, ml.quicL = nil, nil
		tryPort := port
		if useQUIC {
			ql, err := e.listenQUIC(bind, tryPort)
			if err != nil {
				lastErr = err
				if !wantTCP {
					break
				}
				// QUIC cannot listen here: serve TCP only.
				e.logger.Printf("[netx] QUIC listen on %s:%d failed (%v); serving TCP only", bind, tryPort, err)
				useQUIC = false
				continue
			}
			ml.quicL = ql
			tryPort = int(ql.Port())
		}
		if wantTCP {
			tl, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(tryPort)))
			if err != nil {
				lastErr = err
				if ml.quicL != nil {
					ml.quicL.Close()
					ml.quicL = nil
				}
				if port != 0 {
					break
				}
				continue // TCP port taken: let MsQuic pick another UDP port
			}
			ml.tcpL = tl
			tryPort = tl.Addr().(*net.TCPAddr).Port
		}
		ml.port = tryPort
		lastErr = nil
		break
	}
	if lastErr != nil {
		cancel()
		return nil, fmt.Errorf("listen %s:%d: %w", bind, port, lastErr)
	}
	if ml.tcpL == nil && ml.quicL == nil {
		cancel()
		return nil, errors.New("listen: no transport available")
	}

	if ml.quicL != nil {
		ml.wg.Add(1)
		go ml.acceptQUIC()
	}
	if ml.tcpL != nil {
		ml.wg.Add(1)
		go ml.acceptTCP()
	}
	e.mu.Lock()
	e.listeners = append(e.listeners, ml)
	e.mu.Unlock()
	return ml, nil
}

func (e *Endpoint) listenQUIC(bind string, port int) (*transport.Listener, error) {
	return e.quic.ListenOn(bind, uint16(port))
}

func (ml *multiListener) Port() int { return ml.port }

func (ml *multiListener) Accept(ctx context.Context) (Conn, error) {
	select {
	case c := <-ml.conns:
		return c, nil
	case <-ml.done:
		return nil, ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (ml *multiListener) deliver(c Conn) {
	select {
	case ml.conns <- c:
	case <-ml.done:
		c.Close()
	}
}

func (ml *multiListener) acceptQUIC() {
	defer ml.wg.Done()
	for {
		c, err := ml.quicL.Accept(ml.ctx)
		if err != nil {
			return
		}
		ml.deliver(&quicConn{c: c})
	}
}

func (ml *multiListener) acceptTCP() {
	defer ml.wg.Done()
	var delay time.Duration
	for {
		raw, err := ml.tcpL.Accept()
		if err != nil {
			select {
			case <-ml.done:
				return
			default:
			}
			if delay == 0 {
				delay = 5 * time.Millisecond
			} else if delay < time.Second {
				delay *= 2
			}
			time.Sleep(delay) // e.g. EMFILE: back off instead of spinning
			continue
		}
		delay = 0
		go ml.handleTCP(raw)
	}
}

func (ml *multiListener) handleTCP(raw net.Conn) {
	if tc, ok := raw.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
	}
	raw.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReaderSize(raw, 4096)
	first, err := br.Peek(1)
	if err != nil {
		raw.Close()
		return
	}
	pc := &peekedConn{Conn: raw, r: br}
	if first[0] != 0x16 { // not a TLS handshake record
		raw.SetReadDeadline(time.Time{})
		if ml.e.opts.Legacy != nil {
			ml.e.opts.Legacy(pc)
		} else {
			raw.Close()
		}
		return
	}
	tc := tls.Server(pc, ml.e.tlsServer)
	if err := tc.HandshakeContext(ml.ctx); err != nil {
		raw.Close()
		return
	}
	raw.SetReadDeadline(time.Time{})
	conn, err := newTCPConn(tc, true)
	if err != nil {
		raw.Close()
		return
	}
	ml.deliver(conn)
}

func (ml *multiListener) Close() error {
	ml.once.Do(func() {
		close(ml.done)
		ml.cancel()
		if ml.tcpL != nil {
			ml.tcpL.Close()
		}
		if ml.quicL != nil {
			ml.quicL.Close()
		}
		ml.wg.Wait()
		for {
			select {
			case c := <-ml.conns:
				c.Close()
			default:
				return
			}
		}
	})
	return nil
}

type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *peekedConn) Read(b []byte) (int, error) { return p.r.Read(b) }

// ===== Dial =====

// Dial connects to "host:port". In auto mode QUIC and TCP race.
func (e *Endpoint) Dial(ctx context.Context, addr string) (Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("dial %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("dial %q: invalid port", addr)
	}
	ctx, cancel := context.WithTimeout(ctx, e.opts.DialTimeout)
	defer cancel()

	switch {
	case e.opts.Mode == ModeTCP || e.quic == nil:
		if e.opts.Mode == ModeQUIC {
			return nil, transport.ErrUnavailable
		}
		return e.dialTCP(ctx, addr)
	case e.opts.Mode == ModeQUIC:
		return e.dialQUIC(ctx, host, port)
	}

	type result struct {
		c   Conn
		err error
	}
	results := make(chan result, 2)
	go func() {
		c, err := e.dialQUIC(ctx, host, port)
		results <- result{c, err}
	}()
	tcpStarted := false
	startTCP := func() {
		tcpStarted = true
		go func() {
			c, err := e.dialTCP(ctx, addr)
			results <- result{c, err}
		}()
	}
	timer := time.NewTimer(e.opts.TCPFallbackDelay)
	defer timer.Stop()

	var errs []error
	pending := 1
	for pending > 0 {
		select {
		case <-timer.C:
			if !tcpStarted {
				startTCP()
				pending++
			}
		case r := <-results:
			pending--
			if r.err == nil {
				// Close the loser, if any, in the background.
				go func(n int) {
					for i := 0; i < n; i++ {
						if late := <-results; late.c != nil {
							late.c.Close()
						}
					}
				}(pending)
				return r.c, nil
			}
			errs = append(errs, r.err)
			if !tcpStarted {
				startTCP()
				pending++
			}
		case <-ctx.Done():
			if pending > 0 {
				go func(n int) {
					for i := 0; i < n; i++ {
						if late := <-results; late.c != nil {
							late.c.Close()
						}
					}
				}(pending)
			}
			return nil, fmt.Errorf("dial %s: %w", addr, ctx.Err())
		}
	}
	return nil, fmt.Errorf("dial %s: %w", addr, errors.Join(errs...))
}

func (e *Endpoint) dialQUIC(ctx context.Context, host string, port int) (Conn, error) {
	c, err := e.quic.Dial(ctx, host, uint16(port))
	if err != nil {
		return nil, err
	}
	return &quicConn{c: c}, nil
}

func (e *Endpoint) dialTCP(ctx context.Context, addr string) (Conn, error) {
	d := net.Dialer{KeepAlive: 30 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tcp dial %s: %w", addr, err)
	}
	if tc, ok := raw.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	cfg := e.tlsClient.Clone()
	if host, _, err := net.SplitHostPort(addr); err == nil && net.ParseIP(host) == nil {
		cfg.ServerName = host
	}
	tc := tls.Client(raw, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, fmt.Errorf("tls handshake %s: %w", addr, err)
	}
	return newTCPConn(tc, false)
}

// ===== certificates =====

func loadOrGenerateCert(certFile, keyFile string) (tls.Certificate, []byte, []byte, error) {
	if certFile != "" && keyFile != "" {
		certPEM, err := os.ReadFile(certFile)
		if err != nil {
			return tls.Certificate{}, nil, nil, fmt.Errorf("read cert: %w", err)
		}
		keyPEM, err := os.ReadFile(keyFile)
		if err != nil {
			return tls.Certificate{}, nil, nil, fmt.Errorf("read key: %w", err)
		}
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return tls.Certificate{}, nil, nil, fmt.Errorf("parse key pair: %w", err)
		}
		return cert, certPEM, keyPEM, nil
	}
	certPEM, keyPEM, err := GenerateSelfSigned("magnetk")
	if err != nil {
		return tls.Certificate{}, nil, nil, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	return cert, certPEM, keyPEM, err
}

// GenerateSelfSigned returns a PEM certificate and key (ECDSA P-256, 2 years).
func GenerateSelfSigned(commonName string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"Magnetk"}},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(2 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{commonName, "localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// writeTempCert stores generated PEM material for MsQuic/OpenSSL, which only
// loads certificates from files. MAGNETK_CERT_DIR overrides the directory
// (default <temp>/magnetk-certs).
func writeTempCert(certPEM, keyPEM []byte) (string, string, error) {
	sum := sha256.Sum256(certPEM)
	dir := os.Getenv("MAGNETK_CERT_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "magnetk-certs")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	id := hex.EncodeToString(sum[:8])
	certPath := filepath.Join(dir, "cert-"+id+".pem")
	keyPath := filepath.Join(dir, "key-"+id+".pem")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}
