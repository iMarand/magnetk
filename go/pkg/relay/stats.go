package relay

import (
	"runtime"
	"sync/atomic"
	"time"
)

type counters struct {
	connects     atomic.Uint64
	authFailures atomic.Uint64
	msgs         atomic.Uint64
	pubs         atomic.Uint64
	fanout       atomic.Uint64
	requests     atomic.Uint64
	dropped      atomic.Uint64
	noRoute      atomic.Uint64
	stored       atomic.Uint64
	rateLimited  atomic.Uint64
	pipesTotal   atomic.Uint64
	pipeBytes    atomic.Uint64
	legacyConns  atomic.Uint64
}

// Stats is a snapshot of relay state, served as JSON to `magnetk stats`.
type Stats struct {
	RelayID       string            `json:"relay_id"`
	Uptime        string            `json:"uptime"`
	UptimeSec     int64             `json:"uptime_sec"`
	QUIC          bool              `json:"quic"`
	Nodes         int               `json:"nodes"`
	RemoteNodes   int               `json:"remote_nodes"`
	Links         []string          `json:"links"`
	Subscriptions int               `json:"subscriptions"`
	Files         int               `json:"files"`
	ActivePipes   int64             `json:"active_pipes"`
	OfflineNodes  int               `json:"offline_nodes"`
	OfflineBytes  int64             `json:"offline_bytes"`
	Counters      map[string]uint64 `json:"counters"`
	Runtime       RuntimeStats      `json:"runtime"`
}

// RuntimeStats describes process resource usage.
type RuntimeStats struct {
	Goroutines int    `json:"goroutines"`
	HeapMB     uint64 `json:"heap_mb"`
	SysMB      uint64 `json:"sys_mb"`
	NumGC      uint32 `json:"num_gc"`
	CPUs       int    `json:"cpus"`
	MaxProcs   int    `json:"gomaxprocs"`
}

// Stats returns a snapshot of the relay state.
func (s *Server) Stats() Stats {
	s.mu.RLock()
	nodes, remote := len(s.sessions), len(s.routes)
	links := make([]string, 0, len(s.links))
	for id, l := range s.links {
		dir := "in"
		if l.outbound {
			dir = "out"
		}
		links = append(links, id+" ("+dir+", "+l.sess.conn.Protocol()+")")
	}
	s.mu.RUnlock()
	offNodes, offBytes := s.offline.stats()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	up := time.Since(s.started)
	return Stats{
		RelayID:       s.cfg.ID,
		Uptime:        up.Round(time.Second).String(),
		UptimeSec:     int64(up.Seconds()),
		QUIC:          s.QUICEnabled(),
		Nodes:         nodes,
		RemoteNodes:   remote,
		Links:         links,
		Subscriptions: s.subs.count(),
		Files:         s.files.count(),
		ActivePipes:   s.pipes.Load(),
		OfflineNodes:  offNodes,
		OfflineBytes:  offBytes,
		Counters: map[string]uint64{
			"connects":      s.st.connects.Load(),
			"auth_failures": s.st.authFailures.Load(),
			"messages":      s.st.msgs.Load(),
			"publishes":     s.st.pubs.Load(),
			"deliveries":    s.st.fanout.Load(),
			"requests":      s.st.requests.Load(),
			"dropped":       s.st.dropped.Load(),
			"no_route":      s.st.noRoute.Load(),
			"stored":        s.st.stored.Load(),
			"rate_limited":  s.st.rateLimited.Load(),
			"pipes":         s.st.pipesTotal.Load(),
			"pipe_bytes":    s.st.pipeBytes.Load(),
			"legacy_conns":  s.st.legacyConns.Load(),
		},
		Runtime: RuntimeStats{
			Goroutines: runtime.NumGoroutine(),
			HeapMB:     ms.HeapAlloc >> 20,
			SysMB:      ms.Sys >> 20,
			NumGC:      ms.NumGC,
			CPUs:       runtime.NumCPU(),
			MaxProcs:   runtime.GOMAXPROCS(0),
		},
	}
}
