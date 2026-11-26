package diag

// HTTPS comparison: the host also serves the same workloads over HTTPS
// (TLS 1.3, HTTP/2, a self-signed certificate the client pins), and the
// "compare" test measures Magnetk and HTTPS back to back on the same
// network: cold connect, small-request latency, concurrent requests,
// download and upload.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iMarand/magnetk/go/pkg/hrtime"
	"github.com/iMarand/magnetk/go/pkg/node"
)

// ===== host side =====

func selfSignedCert() (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "magnetk-diag"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"magnetk-diag"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	sum := sha256.Sum256(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, hex.EncodeToString(sum[:]), nil
}

func (h *Host) startHTTPS(addr string) error {
	cert, fp, err := selfSignedCert()
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("pong")) })
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		w.Write(b)
	})
	mux.HandleFunc("/source", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
		if n <= 0 || n > 1<<30 {
			http.Error(w, "bytes=1..1GiB", 400)
			return
		}
		w.Header().Set("Trailer", "X-Sha256") // checksum after the body, like the Magnetk source service
		sum := sha256.New()
		for left := n; left > 0; {
			chunk := h.block[:min(int64(len(h.block)), left)]
			if _, err := w.Write(chunk); err != nil {
				return
			}
			sum.Write(chunk)
			left -= int64(len(chunk))
		}
		w.Header().Set("X-Sha256", hex.EncodeToString(sum.Sum(nil)))
	})
	mux.HandleFunc("/sink", func(w http.ResponseWriter, r *http.Request) {
		sum := sha256.New()
		t0 := time.Now()
		n, err := io.Copy(sum, r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Write(mustJSON(sinkResult{Bytes: n, SHA256: hex.EncodeToString(sum.Sum(nil)), Ms: float64(time.Since(t0).Microseconds()) / 1000}))
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("diag https: %w", err)
	}
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"h2", "http/1.1"}},
	}
	h.https, h.httpsFP = srv, fp
	h.httpsPort = ":" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	go srv.ServeTLS(ln, "", "")
	return nil
}

// ===== client side =====

// httpsClient returns a client that pins the host's certificate.
func httpsClient(fp string) *http.Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: true, //nolint:gosec // verified below by pinning the fingerprint
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 {
					return errors.New("no certificate")
				}
				sum := sha256.Sum256(raw[0])
				if hex.EncodeToString(sum[:]) != fp {
					return errors.New("https certificate does not match the host's")
				}
				return nil
			},
		},
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     30 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: 2 * time.Minute}
}

type sideResult struct {
	ColdConnect latency `json:"cold_connect"`
	Request64B  latency `json:"request_64B"`
	Concurrent  latency `json:"concurrent_16"`
	ReqPerS     float64 `json:"requests_per_s"`
	DownMBps    float64 `json:"download_MBps"`
	UpMBps      float64 `json:"upload_MBps"`
	Protocol    string  `json:"protocol,omitempty"`
}

func (c *Client) testCompare(ctx context.Context, m map[string]any) error {
	var h hostHello
	if err := c.ctl(ctx, ctlReq{Op: "hello", Info: c.cfg.Device}, &h); err != nil {
		return err
	}
	if h.HTTPS == "" {
		return errors.New("the host has no HTTPS server (start it with -https)")
	}
	relayHost, _, _ := net.SplitHostPort(c.node.Info().Relay)
	if ip := net.ParseIP(relayHost); ip != nil && ip.IsLoopback() {
		relayHost = "127.0.0.1"
	}
	base := "https://" + net.JoinHostPort(relayHost, strings.TrimPrefix(h.HTTPS, ":"))
	size := int64(c.cfg.PipeMB) << 20
	hc := httpsClient(h.HTTPSCert)
	defer hc.CloseIdleConnections()

	var mk, hs sideResult
	payload := make([]byte, 64)
	rand.Read(payload)
	topic := echoTopic(c.cfg.HostID)

	// 1. cold connect: everything from nothing to the first answer
	var mkCold, hsCold []time.Duration
	for i := 0; i < 3 && ctx.Err() == nil; i++ {
		t0 := hrtime.Now()
		n, err := node.New(node.Config{Relays: []string{c.node.Info().Relay}, Token: c.cfg.Token, Transport: c.cfg.Transport, CacheDir: "-", ID: fmt.Sprintf("%s-cold%d", c.cfg.ID, i)})
		if err == nil {
			if err = n.Start(ctx); err == nil {
				_, err = n.RequestTopic(ctx, topic, payload)
			}
			n.Close()
		}
		if err != nil {
			return fmt.Errorf("magnetk cold connect: %w", err)
		}
		mkCold = append(mkCold, hrtime.Since(t0))

		cold := httpsClient(h.HTTPSCert)
		t1 := hrtime.Now()
		resp, err := cold.Get(base + "/ping")
		if err != nil {
			return fmt.Errorf("https cold connect to %s: %w", base, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		hsCold = append(hsCold, hrtime.Since(t1))
		hs.Protocol = resp.Proto
		cold.CloseIdleConnections()
	}
	mk.ColdConnect, hs.ColdConnect = summarize(mkCold), summarize(hsCold)

	// 2. sequential small requests on a warm connection (alternating)
	var mkSeq, hsSeq []time.Duration
	if _, err := hc.Get(base + "/ping"); err != nil { // warm up
		return err
	}
	for i := 0; i < 100 && ctx.Err() == nil; i++ {
		t0 := hrtime.Now()
		if _, err := c.node.RequestTopic(ctx, topic, payload); err != nil {
			return fmt.Errorf("magnetk request: %w", err)
		}
		mkSeq = append(mkSeq, hrtime.Since(t0))
		t1 := hrtime.Now()
		if err := httpsEcho(hc, base, payload); err != nil {
			return err
		}
		hsSeq = append(hsSeq, hrtime.Since(t1))
	}
	mk.Request64B, hs.Request64B = summarize(mkSeq), summarize(hsSeq)

	// 3. 16 concurrent clients x 25 requests
	run16 := func(do func() error) ([]time.Duration, float64, error) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		var out []time.Duration
		var ferr error
		t0 := time.Now()
		for w := 0; w < 16; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 25; i++ {
					t := hrtime.Now()
					if err := do(); err != nil {
						mu.Lock()
						ferr = err
						mu.Unlock()
						return
					}
					mu.Lock()
					out = append(out, hrtime.Since(t))
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		return out, round2(float64(len(out)) / time.Since(t0).Seconds()), ferr
	}
	d, rps, err := run16(func() error { _, err := c.node.RequestTopic(ctx, topic, payload); return err })
	if err != nil {
		return err
	}
	mk.Concurrent, mk.ReqPerS = summarize(d), rps
	d, rps, err = run16(func() error { return httpsEcho(hc, base, payload) })
	if err != nil {
		return err
	}
	hs.Concurrent, hs.ReqPerS = summarize(d), rps

	// 4. download and upload, same size, both verified with SHA-256
	dm := map[string]any{}
	// two rounds, alternating which side goes first; the best of each is kept
	// so one unlucky moment on the radio does not decide the result
	for round := 0; round < 2 && ctx.Err() == nil; round++ {
		mkDown := func() error {
			if err := c.testDownload(ctx, node.RouteAuto, dm); err != nil {
				return fmt.Errorf("magnetk download: %w", err)
			}
			mk.DownMBps = max(mk.DownMBps, asFloat(dm["MBps"]))
			return nil
		}
		hsDown := func() error {
			v, err := httpsDownload(hc, base, size)
			hs.DownMBps = max(hs.DownMBps, v)
			return err
		}
		mkUp := func() error {
			um := map[string]any{}
			if err := c.testUpload(ctx, node.RouteAuto, um); err != nil {
				return fmt.Errorf("magnetk upload: %w", err)
			}
			mk.UpMBps = max(mk.UpMBps, asFloat(um["MBps"]))
			return nil
		}
		hsUp := func() error {
			v, err := httpsUpload(hc, base, size, c.block)
			hs.UpMBps = max(hs.UpMBps, v)
			return err
		}
		steps := []func() error{mkDown, hsDown, mkUp, hsUp}
		if round == 1 {
			steps = []func() error{hsDown, mkDown, hsUp, mkUp}
		}
		for _, step := range steps {
			if err := step(); err != nil {
				return err
			}
		}
	}

	m["magnetk"], m["https"], m["bytes"] = mk, hs, size
	m["magnetk_route"] = fmt.Sprintf("%s via %s (direct=%v)", c.node.Info().Protocol, c.node.Info().Relay, dm["direct"])
	m["https_url"] = base
	c.logf("INFO", "compare (%s each):              magnetk      https(%s)", fmtBytes(size), hs.Protocol)
	row := func(name string, a, b float64, unit string, lowerBetter bool) {
		win := "magnetk"
		if (lowerBetter && b < a) || (!lowerBetter && b > a) {
			win = "https"
		}
		c.logf("INFO", "compare %-22s %9.2f %-5s %9.2f %-5s -> %s", name, a, unit, b, unit, win)
	}
	row("cold connect p50", mk.ColdConnect.P50, hs.ColdConnect.P50, "ms", true)
	row("request 64B p50", mk.Request64B.P50, hs.Request64B.P50, "ms", true)
	row("request 64B p90", mk.Request64B.P90, hs.Request64B.P90, "ms", true)
	row("16 concurrent", mk.ReqPerS, hs.ReqPerS, "req/s", false)
	row("download", mk.DownMBps, hs.DownMBps, "MB/s", false)
	row("upload", mk.UpMBps, hs.UpMBps, "MB/s", false)
	return nil
}

func asFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}

func fmtBytes(n int64) string { return fmt.Sprintf("%d MiB", n>>20) }

func httpsEcho(hc *http.Client, base string, payload []byte) error {
	resp, err := hc.Post(base+"/echo", "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("https request: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || !bytes.Equal(b, payload) {
		return fmt.Errorf("https echo corrupted (%v)", err)
	}
	return nil
}

func httpsDownload(hc *http.Client, base string, size int64) (float64, error) {
	t0 := time.Now()
	resp, err := hc.Get(fmt.Sprintf("%s/source?bytes=%d", base, size))
	if err != nil {
		return 0, fmt.Errorf("https download: %w", err)
	}
	defer resp.Body.Close()
	sum := sha256.New()
	n, err := io.Copy(sum, resp.Body)
	if err != nil || n != size {
		return 0, fmt.Errorf("https download: got %d of %d bytes (%v)", n, size, err)
	}
	el := time.Since(t0)
	if resp.Trailer.Get("X-Sha256") != hex.EncodeToString(sum.Sum(nil)) {
		return 0, errors.New("https download: sha256 mismatch")
	}
	return mbps(size, el), nil
}

func httpsUpload(hc *http.Client, base string, size int64, block []byte) (float64, error) {
	pr, pw := io.Pipe()
	sum := sha256.New()
	go func() {
		for left := size; left > 0; {
			chunk := block[:min(int64(len(block)), left)]
			if _, err := pw.Write(chunk); err != nil {
				return
			}
			sum.Write(chunk)
			left -= int64(len(chunk))
		}
		pw.Close()
	}()
	t0 := time.Now()
	req, _ := http.NewRequest(http.MethodPost, base+"/sink", pr)
	req.ContentLength = size
	resp, err := hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("https upload: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	el := time.Since(t0)
	var sr sinkResult
	if err := json.Unmarshal(b, &sr); err != nil || sr.Bytes != size || sr.SHA256 != hex.EncodeToString(sum.Sum(nil)) {
		return 0, fmt.Errorf("https upload not verified: %s", b)
	}
	return mbps(size, el), nil
}
