package diag

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/iMarand/magnetk/go/pkg/netx"
)

func TestHostClientSuite(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	h, err := StartHost(ctx, HostConfig{
		RelayListen: "127.0.0.1:0", DirectListen: "127.0.0.1:0", HTTP: "-",
		Token: "tok", FileSize: 2 << 20, DataDir: dir, Out: io.Discard, HTTPS: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	relay := fmt.Sprintf("127.0.0.1:%d", h.RelayPort())

	for _, mode := range []netx.Mode{netx.ModeTCP, netx.ModeAuto} {
		t.Run(string(mode), func(t *testing.T) {
			c, err := NewClient(ClientConfig{Relays: []string{relay}, Token: "tok", Transport: mode, PipeMB: 4, Messages: 5000, DataDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if err := c.Start(ctx); err != nil {
				t.Fatal(err)
			}
			for _, r := range c.Run([]string{"all"}, 1) {
				if !r.OK {
					t.Errorf("%s failed: %s (%v)", r.Test, r.Error, r.Metrics)
				}
			}
		})
	}
}
