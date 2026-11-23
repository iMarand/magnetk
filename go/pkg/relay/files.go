package relay

import (
	"sync"
	"time"

	"github.com/iMarand/magnetk/go/pkg/wire"
)

// fileRegistry records which nodes provide which content hash.
type fileRegistry struct {
	mu     sync.RWMutex
	files  map[string]*fileEntry      // hash -> entry
	byNode map[string]map[string]bool // provider -> hashes
}

type fileEntry struct {
	hash      string
	name      string
	size      uint64
	chunkSize uint32
	root      string
	created   time.Time
	providers map[string]string // provider node -> relay id ("" = local)
}

func newFileRegistry() *fileRegistry {
	return &fileRegistry{files: make(map[string]*fileEntry), byNode: make(map[string]map[string]bool)}
}

func (r *fileRegistry) publish(m wire.FilePublish, viaRelay string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.files[m.Hash]
	if e == nil {
		e = &fileEntry{hash: m.Hash, created: time.Now(), providers: make(map[string]string)}
		r.files[m.Hash] = e
	}
	e.name, e.size, e.chunkSize = m.Name, m.Size, m.ChunkSize
	if m.Root != "" {
		e.root = m.Root
	}
	e.providers[m.Provider] = viaRelay
	if r.byNode[m.Provider] == nil {
		r.byNode[m.Provider] = make(map[string]bool)
	}
	r.byNode[m.Provider][m.Hash] = true
}

func (r *fileRegistry) unpublish(provider, hash string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unpublishLocked(provider, hash)
}

func (r *fileRegistry) unpublishLocked(provider, hash string) {
	if e := r.files[hash]; e != nil {
		delete(e.providers, provider)
		if len(e.providers) == 0 {
			delete(r.files, hash)
		}
	}
	if hs := r.byNode[provider]; hs != nil {
		delete(hs, hash)
		if len(hs) == 0 {
			delete(r.byNode, provider)
		}
	}
}

// removeProvider drops all files of a node and returns their hashes.
func (r *fileRegistry) removeProvider(provider string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	hs := r.byNode[provider]
	out := make([]string, 0, len(hs))
	for h := range hs {
		out = append(out, h)
	}
	for _, h := range out {
		r.unpublishLocked(provider, h)
	}
	return out
}

// removeRelay drops all providers learned through a relay link.
func (r *fileRegistry) removeRelay(relayID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for h, e := range r.files {
		for p, via := range e.providers {
			if via == relayID {
				r.unpublishLocked(p, h)
			}
		}
	}
}

// localPublications returns everything published by local nodes (for
// syncing a newly connected relay link).
func (r *fileRegistry) localPublications() []wire.FilePublish {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []wire.FilePublish
	for _, e := range r.files {
		for p, via := range e.providers {
			if via == "" {
				out = append(out, wire.FilePublish{Provider: p, Hash: e.hash, Name: e.name, Size: e.size, ChunkSize: e.chunkSize, Root: e.root})
			}
		}
	}
	return out
}

func (r *fileRegistry) find(hash string) (*fileEntry, []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e := r.files[hash]
	if e == nil {
		return nil, nil
	}
	cp := *e
	providers := make([]string, 0, len(e.providers))
	// local providers first: they are one hop closer
	for p, via := range e.providers {
		if via == "" {
			providers = append(providers, p)
		}
	}
	for p, via := range e.providers {
		if via != "" {
			providers = append(providers, p)
		}
	}
	return &cp, providers
}

func (r *fileRegistry) count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.files)
}

// ===== offline store =====

// offlineStore keeps messages sent with FlagStore for nodes that are not
// connected, bounded per node, in total, and by age.
type offlineStore struct {
	mu         sync.Mutex
	byNode     map[string][]storedMsg
	total      int64
	maxPerNode int
	maxTotal   int64
	ttl        time.Duration
}

type storedMsg struct {
	frame []byte
	at    time.Time
}

func newOfflineStore(perNode int, total int64, ttl time.Duration) *offlineStore {
	return &offlineStore{byNode: make(map[string][]storedMsg), maxPerNode: perNode, maxTotal: total, ttl: ttl}
}

func (o *offlineStore) put(node string, frame []byte) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.total+int64(len(frame)) > o.maxTotal {
		return false
	}
	q := o.byNode[node]
	if len(q) >= o.maxPerNode {
		// drop the oldest to make room: newest data is usually most relevant
		o.total -= int64(len(q[0].frame))
		q = q[1:]
	}
	o.byNode[node] = append(q, storedMsg{frame: frame, at: time.Now()})
	o.total += int64(len(frame))
	return true
}

func (o *offlineStore) take(node string) [][]byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	q := o.byNode[node]
	if len(q) == 0 {
		return nil
	}
	delete(o.byNode, node)
	cutoff := time.Now().Add(-o.ttl)
	out := make([][]byte, 0, len(q))
	for _, m := range q {
		o.total -= int64(len(m.frame))
		if m.at.After(cutoff) {
			out = append(out, m.frame)
		}
	}
	return out
}

func (o *offlineStore) sweep() {
	o.mu.Lock()
	defer o.mu.Unlock()
	cutoff := time.Now().Add(-o.ttl)
	for node, q := range o.byNode {
		i := 0
		for i < len(q) && q[i].at.Before(cutoff) {
			o.total -= int64(len(q[i].frame))
			i++
		}
		if i == len(q) {
			delete(o.byNode, node)
		} else if i > 0 {
			o.byNode[node] = q[i:]
		}
	}
}

func (o *offlineStore) stats() (nodes int, bytes int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.byNode), o.total
}
