package relay

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/iMarand/magnetk/go/pkg/identity"
	"github.com/iMarand/magnetk/go/pkg/wire"
)

// helloMaxSkew bounds the HELLO timestamp against the relay clock, so a
// captured signed HELLO cannot be replayed later.
const helloMaxSkew = 10 * time.Minute

// maxBindings bounds the registry (oldest entries are not evicted: a full
// registry stops binding new names, it never unbinds).
const maxBindings = 1_000_000

// identities binds node ids to the first Ed25519 key that claimed them.
type identities struct {
	path string
	mu   sync.Mutex
	m    map[string]binding
}

type binding struct {
	Key   string    `json:"key"` // fingerprint
	Since time.Time `json:"since"`
}

func openIdentities(path string) (*identities, error) {
	ids := &identities{path: path, m: map[string]binding{}}
	if path == "" {
		return ids, nil
	}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &ids.m); err != nil {
			return nil, fmt.Errorf("relay: identities file %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return ids, nil
}

// check authenticates a HELLO. It returns the node's key fingerprint ("" for
// a node without a key) or a rejection message.
func (ids *identities) check(h wire.Hello, id string, now time.Time, require bool) (fp string, reject string) {
	if len(h.PubKey) == 0 {
		if require {
			return "", "this relay requires a node identity key (magnetk 2.1+)"
		}
		ids.mu.Lock()
		b, bound := ids.m[id]
		ids.mu.Unlock()
		if bound && h.NodeID != "" {
			return "", fmt.Sprintf("node id %q is registered to key %s: connect with that key", id, identity.Short(b.Key))
		}
		return "", ""
	}
	ts := time.UnixMilli(int64(h.TS))
	if d := now.Sub(ts); d > helloMaxSkew || d < -helloMaxSkew {
		return "", fmt.Sprintf("hello timestamp is %s off the relay clock (check the device time)", d.Round(time.Second))
	}
	if !identity.Verify(h.PubKey, identity.HelloPayload(h.NodeID, h.TS), h.Sig) {
		return "", "invalid identity signature"
	}
	fp = identity.Fingerprint(h.PubKey)
	if h.NodeID == "" { // relay-assigned id: nothing to bind
		return fp, ""
	}
	ids.mu.Lock()
	defer ids.mu.Unlock()
	if b, ok := ids.m[id]; ok {
		if b.Key != fp {
			return "", fmt.Sprintf("node id %q belongs to another key (%s); yours is %s", id, identity.Short(b.Key), identity.Short(fp))
		}
		return fp, ""
	}
	if len(ids.m) < maxBindings {
		ids.m[id] = binding{Key: fp, Since: now.UTC()}
		ids.saveLocked()
	}
	return fp, ""
}

// lookup returns the key bound to id ("" if none).
func (ids *identities) lookup(id string) string {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	return ids.m[id].Key
}

func (ids *identities) saveLocked() {
	if ids.path == "" {
		return
	}
	b, _ := json.MarshalIndent(ids.m, "", "  ")
	os.MkdirAll(filepath.Dir(ids.path), 0o700)
	tmp := ids.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, ids.path)
	}
}
