package node

import (
	"context"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/relay"
	"github.com/iMarand/magnetk/go/pkg/transport"
)

// TestRelayPipeThroughputStable checks that relayed pipe throughput stays
// high across uploads and downloads on the same relay connections
// (regression test: after a relayed download, later pipes ran at 2-5 MB/s).
func TestRelayPipeThroughputStable(t *testing.T) {
	if testing.Short() {
		t.Skip("throughput test")
	}
	qc := transport.Config{Congestion: transport.ParseCongestion(os.Getenv("MK_TEST_CC"))}
	r := startRelay(t, relay.Config{QUIC: qc})
	host := startNode(t, Config{Relays: []string{addr(r)}, ID: "host", Transport: netx.ModeQUIC, QUIC: qc, Listen: true, ListenAddr: "127.0.0.1:0", WatchPresence: true})
	const size = 16 << 20
	block := make([]byte, 1<<20)
	host.HandlePipe("sink", func(p *Pipe) {
		defer p.Close()
		p.SetDeadline(time.Now().Add(time.Minute))
		io.Copy(io.Discard, p)
		p.Write([]byte("ok"))
		p.CloseWrite()
		io.Copy(io.Discard, p)
	})
	host.HandlePipe("source", func(p *Pipe) {
		defer p.Close()
		p.SetDeadline(time.Now().Add(time.Minute))
		var one [1]byte
		io.ReadFull(p, one[:])
		for i := 0; i < size/len(block); i++ {
			if _, err := p.Write(block); err != nil {
				return
			}
		}
		p.Write(make([]byte, 32))
		p.CloseWrite()
		io.Copy(io.Discard, p)
	})
	stats := func() string {
		host.mu.RLock()
		rc := host.cur
		host.mu.RUnlock()
		if q, ok := rc.conn.(interface{ Stats() transport.ConnStats }); ok {
			s := q.Stats()
			return "cwnd=" + itoa(int(s.CongestionWindow)) + " lost=" + itoa(int(s.LostPackets)) + " rtt=" + s.RTT.String()
		}
		return ""
	}
	for _, mode := range []netx.Mode{netx.ModeQUIC, netx.ModeTCP} {
		c := startNode(t, Config{Relays: []string{addr(r)}, Transport: mode, QUIC: qc})
		for _, dir := range []string{"up", "down", "up", "down", "up"} {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			t0 := time.Now()
			if dir == "up" {
				p, err := c.OpenPipe(ctx, "host", "sink", WithRoute(RouteRelay))
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < size/len(block); i++ {
					if _, err := p.Write(block); err != nil {
						t.Fatal(err)
					}
				}
				p.CloseWrite()
				io.ReadAll(p)
				p.Close()
			} else {
				p, err := c.OpenPipe(ctx, "host", "source", WithRoute(RouteRelay))
				if err != nil {
					t.Fatal(err)
				}
				p.Write([]byte{1})
				if _, err := io.ReadFull(p, make([]byte, size+32)); err != nil {
					t.Fatal(err)
				}
				if gracefulClose {
					p.CloseWrite()
					io.Copy(io.Discard, p)
				}
				p.Close()
			}
			cancel()
			mbps := float64(size) / time.Since(t0).Seconds() / 1e6
			t.Logf("%s %s: %.1f MB/s  host %s", mode, dir, mbps, stats())
			if mbps < 15 {
				t.Errorf("%s %s: relayed pipe ran at %.1f MB/s", mode, dir, mbps)
			}
		}
		c.Close()
	}
}

var gracefulClose = true

func itoa(i int) string { return strconv.Itoa(i) }
