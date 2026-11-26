package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/iMarand/magnetk/go/pkg/config"
	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/relay"
)

// RelayMain runs a relay until interrupted.
func RelayMain(args []string) error {
	fs := flag.NewFlagSet("relay", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultPath, "settings file")
	listen := fs.String("listen", "", "QUIC+TCP listen address (default from RELAY_PORT/BIND_ADDRESS, 0.0.0.0:4002)")
	port := fs.Int("port", 0, "listen port (shortcut for -listen 0.0.0.0:PORT)")
	bind := fs.String("bind", "", "bind address")
	legacy := fs.String("legacy", "", "Magnetk v1 (JS SDK) listen address, or \"off\"")
	id := fs.String("id", "", "relay id (used in federations)")
	peers := fs.String("peers", "", "other relays to federate with (host:port, comma separated)")
	secret := fs.String("secret", "", "cluster secret shared by federated relays")
	tokens := fs.String("tokens", "", "accepted node tokens (comma separated; empty = open relay)")
	mode := fs.String("transport", "", "auto | quic | tcp")
	httpAddr := fs.String("http", "", "serve /stats, /metrics and /healthz on this address (e.g. 127.0.0.1:8090)")
	statsEvery := fs.Duration("stats-interval", time.Minute, "log a stats line this often (0 = never)")
	verbose := fs.Bool("v", false, "log every connection")
	idsFile := fs.String("identities", "", "file binding node ids to identity keys (default IDENTITIES_FILE or relay-identities.json; \"-\" = memory only)")
	requireID := fs.Bool("require-identity", false, "reject nodes without an identity key")
	if err := fs.Parse(args); err != nil {
		return err
	}

	file, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	file.ApplyRuntime()
	cfg := file.Relay()
	if *idsFile != "" {
		cfg.IdentitiesFile = *idsFile
	} else if cfg.IdentitiesFile == "" {
		cfg.IdentitiesFile = "relay-identities.json"
	}
	if cfg.IdentitiesFile == "-" {
		cfg.IdentitiesFile = ""
	}
	if *requireID {
		cfg.RequireIdentity = true
	}
	if *bind != "" {
		_, p, _ := net.SplitHostPort(cfg.Listen)
		cfg.Listen = net.JoinHostPort(*bind, p)
	}
	if *port != 0 {
		h, _, _ := net.SplitHostPort(cfg.Listen)
		cfg.Listen = net.JoinHostPort(h, fmt.Sprint(*port))
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *legacy != "" {
		cfg.LegacyListen = *legacy
		if strings.EqualFold(*legacy, "off") {
			cfg.LegacyListen = ""
		}
	}
	if *id != "" {
		cfg.ID = *id
	}
	if *peers != "" {
		cfg.Peers = splitList(*peers)
	}
	if *secret != "" {
		cfg.ClusterSecret = *secret
	}
	if *tokens != "" {
		cfg.AuthTokens = splitList(*tokens)
	}
	if *mode != "" {
		cfg.Mode = netx.ParseMode(*mode)
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	if *verbose {
		cfg.Logger = logger
	}
	if len(cfg.Peers) > 0 && cfg.ClusterSecret == "" {
		return fmt.Errorf("federation needs a cluster secret (-secret or CLUSTER_SECRET)")
	}

	srv, err := relay.New(cfg)
	if err != nil {
		return err
	}
	if err := srv.Start(); err != nil {
		return err
	}
	defer srv.Close()

	fmt.Println("Magnetk relay", Version)
	fmt.Printf("  id         %s\n", srv.ID())
	fmt.Printf("  listening  %s  (QUIC/UDP: %v, TCP: %v)\n", srv.Addr(), srv.QUICEnabled(), cfg.Mode != netx.ModeQUIC)
	if cfg.LegacyListen != "" {
		fmt.Printf("  legacy v1  %s\n", cfg.LegacyListen)
	}
	if len(cfg.AuthTokens) > 0 {
		fmt.Printf("  auth       %d token(s) required\n", len(cfg.AuthTokens))
	} else {
		fmt.Println("  auth       open (set AUTH_TOKENS to restrict)")
	}
	if len(cfg.Peers) > 0 {
		fmt.Printf("  federation %s\n", strings.Join(cfg.Peers, ", "))
	}

	if *httpAddr != "" {
		go serveHTTP(*httpAddr, srv, logger)
		fmt.Printf("  http       http://%s/stats\n", *httpAddr)
	}
	if *statsEvery > 0 {
		go func() {
			t := time.NewTicker(*statsEvery)
			defer t.Stop()
			for range t.C {
				st := srv.Stats()
				logger.Printf("nodes=%d remote=%d links=%d subs=%d files=%d pipes=%d msgs=%d pubs=%d dropped=%d heap=%dMB goroutines=%d",
					st.Nodes, st.RemoteNodes, len(st.Links), st.Subscriptions, st.Files, st.ActivePipes,
					st.Counters["messages"], st.Counters["publishes"], st.Counters["dropped"], st.Runtime.HeapMB, st.Runtime.Goroutines)
			}
		}()
	}
	fmt.Println("Relay running. Press Ctrl+C to stop.")
	waitSignal()
	fmt.Println("Shutting down...")
	return nil
}

func serveHTTP(addr string, srv *relay.Server, logger *log.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(srv.Stats())
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		st := srv.Stats()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		g := func(name string, v any) { fmt.Fprintf(w, "magnetk_%s %v\n", name, v) }
		g("nodes", st.Nodes)
		g("remote_nodes", st.RemoteNodes)
		g("links", len(st.Links))
		g("subscriptions", st.Subscriptions)
		g("files", st.Files)
		g("active_pipes", st.ActivePipes)
		g("offline_bytes", st.OfflineBytes)
		g("goroutines", st.Runtime.Goroutines)
		g("heap_bytes", st.Runtime.HeapMB<<20)
		for k, v := range st.Counters {
			fmt.Fprintf(w, "magnetk_%s_total %d\n", k, v)
		}
	})
	s := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := s.ListenAndServe(); err != nil {
		logger.Printf("http: %v", err)
	}
}
