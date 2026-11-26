package node

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iMarand/magnetk/go/pkg/identity"
	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/relay"
)

func tryStart(cfg Config) error {
	cfg.CacheDir = "-"
	n, err := New(cfg)
	if err != nil {
		return err
	}
	defer n.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return n.Start(ctx)
}

func TestIdentityBindsNodeID(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ids.json")
	r := startRelay(t, relay.Config{IdentitiesFile: file})
	a, b := identity.Generate(), identity.Generate()
	base := Config{Relays: []string{addr(r)}, ID: "alice", Transport: netx.ModeTCP}

	cfg := base
	cfg.Key = a
	first := startNode(t, cfg) // binds alice -> a
	first.Close()

	cfg.Key = b
	if err := tryStart(cfg); err == nil || !strings.Contains(err.Error(), "belongs to another key") {
		t.Fatalf("other key: %v", err)
	}
	cfg.Key = nil
	if err := tryStart(cfg); err == nil || !strings.Contains(err.Error(), "registered to key") {
		t.Fatalf("no key: %v", err)
	}
	cfg.Key = a
	if err := tryStart(cfg); err != nil {
		t.Fatalf("owner rejected: %v", err)
	}
	// unbound ids and keyless nodes still work; the binding survives a restart
	free := base
	free.ID = "bob"
	if err := tryStart(free); err != nil {
		t.Fatalf("keyless free id: %v", err)
	}
	r.Close()
	r2 := startRelay(t, relay.Config{IdentitiesFile: file})
	cfg.Relays, cfg.Key = []string{addr(r2)}, b
	if err := tryStart(cfg); err == nil {
		t.Fatal("binding lost after relay restart")
	}
	// RequireIdentity rejects keyless nodes
	r3 := startRelay(t, relay.Config{RequireIdentity: true})
	free.Relays = []string{addr(r3)}
	if err := tryStart(free); err == nil || !strings.Contains(err.Error(), "requires a node identity") {
		t.Fatalf("require identity: %v", err)
	}
}
