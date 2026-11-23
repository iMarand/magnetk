package relay

import (
	"sync"
	"sync/atomic"

	"github.com/iMarand/magnetk/go/pkg/wire"
)

// subIndex maps topics to subscribed sessions. Exact topics are a map
// lookup; wildcard patterns are matched by scanning (usually few).
type subIndex struct {
	mu    sync.RWMutex
	exact map[string]map[*session]string // topic -> session -> queue group
	wild  map[string]map[*session]string // pattern -> session -> queue group
	rr    atomic.Uint64                  // round-robin cursor for queue groups
}

func newSubIndex() *subIndex {
	return &subIndex{
		exact: make(map[string]map[*session]string),
		wild:  make(map[string]map[*session]string),
	}
}

func (x *subIndex) table(pattern string) map[string]map[*session]string {
	if wire.IsWildcard(pattern) {
		return x.wild
	}
	return x.exact
}

// add returns true when pattern gained its first local subscriber.
func (x *subIndex) add(ss *session, pattern, queue string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	t := x.table(pattern)
	m := t[pattern]
	first := m == nil
	if first {
		m = make(map[*session]string)
		t[pattern] = m
	}
	m[ss] = queue
	return first
}

// remove returns true when pattern lost its last local subscriber.
func (x *subIndex) remove(ss *session, pattern string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	t := x.table(pattern)
	m := t[pattern]
	if m == nil {
		return false
	}
	delete(m, ss)
	if len(m) == 0 {
		delete(t, pattern)
		return true
	}
	return false
}

// removeAll drops every subscription of a session and returns the patterns
// that no longer have local subscribers.
func (x *subIndex) removeAll(ss *session) []string {
	ss.subsMu.Lock()
	patterns := make([]string, 0, len(ss.subs))
	for p := range ss.subs {
		patterns = append(patterns, p)
	}
	ss.subs = map[string]string{}
	ss.subsMu.Unlock()

	var gone []string
	for _, p := range patterns {
		if x.remove(ss, p) {
			gone = append(gone, p)
		}
	}
	return gone
}

// patterns lists every pattern with local subscribers.
func (x *subIndex) patterns() []string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := make([]string, 0, len(x.exact)+len(x.wild))
	for p := range x.exact {
		out = append(out, p)
	}
	for p := range x.wild {
		out = append(out, p)
	}
	return out
}

func (x *subIndex) count() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	n := 0
	for _, m := range x.exact {
		n += len(m)
	}
	for _, m := range x.wild {
		n += len(m)
	}
	return n
}

// match returns the sessions that should receive a publication: every plain
// subscriber plus one member of each queue group not already served by
// another relay. It also returns the queue groups it served.
func (x *subIndex) match(topic string, served map[string]bool) ([]*session, []string) {
	x.mu.RLock()
	defer x.mu.RUnlock()

	var out []*session
	seen := make(map[*session]bool)
	var groups map[string][]*session

	collect := func(m map[*session]string) {
		for ss, q := range m {
			if q == "" {
				if !seen[ss] {
					seen[ss] = true
					out = append(out, ss)
				}
				continue
			}
			if served[q] {
				continue
			}
			if groups == nil {
				groups = make(map[string][]*session)
			}
			groups[q] = append(groups[q], ss)
		}
	}
	if m := x.exact[topic]; m != nil {
		collect(m)
	}
	for p, m := range x.wild {
		if wire.MatchTopic(p, topic) {
			collect(m)
		}
	}

	var servedNow []string
	for q, members := range groups {
		pick := members[int(x.rr.Add(1)%uint64(len(members)))]
		if !seen[pick] {
			seen[pick] = true
			out = append(out, pick)
		}
		servedNow = append(servedNow, q)
	}
	return out, servedNow
}

// pickOne chooses a single subscriber for a topic request, preferring queue
// group members, rotating between candidates.
func (x *subIndex) pickOne(topic string) *session {
	x.mu.RLock()
	defer x.mu.RUnlock()
	var cands []*session
	add := func(m map[*session]string) {
		for ss := range m {
			cands = append(cands, ss)
		}
	}
	if m := x.exact[topic]; m != nil {
		add(m)
	}
	for p, m := range x.wild {
		if wire.MatchTopic(p, topic) {
			add(m)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	return cands[int(x.rr.Add(1)%uint64(len(cands)))]
}
