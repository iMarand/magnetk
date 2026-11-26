// Package cli implements the magnetk command-line tool. The relay, seed and
// peer binaries are thin wrappers around it that keep their v1 flag names.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/iMarand/magnetk/go/pkg/config"
	"github.com/iMarand/magnetk/go/pkg/identity"
	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/node"
	"github.com/iMarand/magnetk/go/pkg/transport"
)

// Version of the tool and protocol.
const Version = "2.0.0"

type command struct {
	name, usage, help string
	run               func(args []string) error
}

var commands []command

func init() {
	commands = []command{
		{"relay", "relay [flags]", "run a relay server (messaging hub, file registry, pipe splicer)", RelayMain},
		{"share", "share [flags] <file>...", "share files and keep serving them", ShareMain},
		{"get", "get [flags] <link|hash>", "download a file", GetMain},
		{"send", "send [flags] <node> <message>", "send a direct message", SendMain},
		{"pub", "pub [flags] <topic> <message>", "publish to a topic", PubMain},
		{"sub", "sub [flags] <pattern>", "print messages for a topic pattern (a.b, a.*, a.>)", SubMain},
		{"req", "req [flags] <node|@topic> <message>", "send a request and print the response", ReqMain},
		{"serve", "serve [flags] <topic>", "answer requests on a topic (echo service, for testing)", ServeMain},
		{"chat", "chat [flags] <node|magnetk://...> [message]", "chat with a node: text and voice notes (-audio file.wav); interactive without a message", ChatMain},
		{"open", "open [-pause] <magnetk://...|magnetk:?...>", "open a magnetk address (chat, relay) or file link (download)", OpenMain},
		{"id", "id [-relay host:port] [-id name]", "show this device's identity key fingerprint and magnetk:// address", IdMain},
		{"install", "install [-uninstall] [-n]", "register magnetk:// and magnetk:? links with this desktop (Windows, macOS, Linux)", InstallMain},
		{"listen", "listen [flags]", "print every direct message and presence event", ListenMain},
		{"nodes", "nodes [flags] [prefix]", "list connected nodes", NodesMain},
		{"ping", "ping [flags]", "measure round trips to the relay", PingMain},
		{"stats", "stats [flags]", "print relay statistics (JSON)", StatsMain},
		{"bench", "bench [flags] <msg|req|pipe|file|all>", "benchmark messaging, pipes and file transfer", BenchMain},
		{"diag", "diag host|client [flags]", "field-test kit: PC host + test client (see android/ for the phone app)", DiagMain},
		{"config", "config init [path]", "write a documented settings.conf", ConfigMain},
		{"version", "version", "print version information", func([]string) error {
			fmt.Printf("magnetk %s (protocol v2, QUIC available: %v)\n", Version, transport.Available())
			return nil
		}},
	}
}

// Main runs the magnetk tool.
func Main(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(os.Stdout)
		return 0
	}
	for _, c := range commands {
		if c.name == args[0] {
			if err := c.run(args[1:]); err != nil {
				fmt.Fprintf(os.Stderr, "magnetk %s: %v\n", c.name, err)
				return 1
			}
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "magnetk: unknown command %q\n\n", args[0])
	usage(os.Stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "Magnetk %s - fast messaging, pipes and file distribution over QUIC\n\nUsage:\n", Version)
	for _, c := range commands {
		fmt.Fprintf(w, "  magnetk %-36s %s\n", c.usage, c.help)
	}
	fmt.Fprintln(w, "\nRun 'magnetk <command> -h' for the flags of a command.")
	fmt.Fprintln(w, "Settings come from ./config/settings.conf and MAGNETK_* environment variables.")
}

// ===== shared node flags =====

type nodeFlags struct {
	fs        *flag.FlagSet
	config    *string
	relays    *string
	id        *string
	token     *string
	transport *string
	route     *string
	direct    *bool
	port      *int
	advertise *string
	verbose   *bool
	timeout   *time.Duration
	identity  *string

	linkRelays *string // share only
}

func newNodeFlags(name string) *nodeFlags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	return &nodeFlags{
		fs:        fs,
		config:    fs.String("config", config.DefaultPath, "settings file"),
		relays:    fs.String("relay", "", "relay address(es) host:port, comma separated (default from RELAYS)"),
		id:        fs.String("id", "", "node id (default: assigned by the relay)"),
		token:     fs.String("token", "", "relay auth token"),
		transport: fs.String("transport", "", "auto | quic | tcp"),
		route:     fs.String("route", "", "data path: auto | direct | relay"),
		direct:    fs.Bool("direct", false, "accept peer-to-peer connections"),
		port:      fs.Int("port", 0, "direct listen port (with -direct)"),
		advertise: fs.String("advertise", "", "public host:port to advertise for direct connections"),
		verbose:   fs.Bool("v", false, "verbose logging"),
		timeout:   fs.Duration("timeout", 15*time.Second, "time to wait for the relay"),
		identity:  fs.String("identity", "", "identity key file (default IDENTITY_FILE or ~/.magnetk/identity.key; \"off\" = none)"),
	}
}

func (f *nodeFlags) parse(args []string) error {
	return f.fs.Parse(args)
}

func (f *nodeFlags) settings() (*config.File, error) {
	file, err := config.Load(*f.config)
	if err != nil {
		return nil, err
	}
	file.ApplyRuntime()
	return file, nil
}

func (f *nodeFlags) nodeConfig(extraRelays []string) (node.Config, error) {
	file, err := f.settings()
	if err != nil {
		return node.Config{}, err
	}
	cfg := file.Node()
	if *f.relays != "" {
		cfg.Relays = splitList(*f.relays)
	}
	if len(cfg.Relays) == 0 {
		cfg.Relays = extraRelays
	}
	if len(cfg.Relays) == 0 {
		return cfg, fmt.Errorf("no relay given: use -relay host:port or set RELAYS in %s", *f.config)
	}
	if *f.id != "" {
		cfg.ID = *f.id
	}
	if *f.token != "" {
		cfg.Token = *f.token
	}
	if *f.transport != "" {
		cfg.Transport = netx.ParseMode(*f.transport)
	}
	if *f.route != "" {
		cfg.Route = node.Route(*f.route)
	}
	if *f.direct {
		cfg.Listen = true
	}
	if *f.port != 0 {
		cfg.Listen = true
		cfg.ListenAddr = fmt.Sprintf("0.0.0.0:%d", *f.port)
	}
	if *f.advertise != "" {
		cfg.AdvertiseAddrs = append(cfg.AdvertiseAddrs, splitList(*f.advertise)...)
	}
	if *f.verbose {
		cfg.Logger = log.New(os.Stderr, "", log.LstdFlags)
	}
	keyPath := *f.identity
	if keyPath == "" {
		keyPath = file.Str("IDENTITY_FILE", defaultKeyPath())
	}
	if keyPath != "off" && keyPath != "-" {
		k, created, err := identity.LoadOrCreate(expandHome(keyPath))
		if err != nil {
			return cfg, fmt.Errorf("identity key %s: %w", keyPath, err)
		}
		if created {
			fmt.Fprintf(os.Stderr, "created identity key %s (fingerprint %s)\n", keyPath, identity.Format(k.Fingerprint()))
		}
		cfg.Key = k
	}
	if f.linkRelays != nil && *f.linkRelays != "" {
		cfg.LinkRelays = splitList(*f.linkRelays)
	}
	return cfg, nil
}

func (f *nodeFlags) start(extraRelays []string) (*node.Node, error) {
	cfg, err := f.nodeConfig(extraRelays)
	if err != nil {
		return nil, err
	}
	n, err := node.New(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *f.timeout)
	defer cancel()
	if err := n.Start(ctx); err != nil {
		n.Close()
		return nil, err
	}
	return n, nil
}

// magnetkHome is ~/.magnetk (keys, known peers, user settings).
func magnetkHome() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".magnetk")
}

func defaultKeyPath() string { return filepath.Join(magnetkHome(), "identity.key") }

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// waitSignal blocks until Ctrl+C / SIGTERM.
func waitSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}

// signalContext is cancelled on Ctrl+C / SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func humanBytes(n float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	return fmt.Sprintf("%.2f %s", n, units[i])
}

// ConfigMain writes a default settings file.
func ConfigMain(args []string) error {
	if len(args) == 0 || args[0] != "init" {
		return fmt.Errorf("usage: magnetk config init [path]")
	}
	path := config.DefaultPath
	if len(args) > 1 {
		path = args[1]
	}
	created, err := config.WriteDefault(path)
	if err != nil {
		return err
	}
	if created {
		fmt.Println("wrote", path)
	} else {
		fmt.Println(path, "already exists (left unchanged)")
	}
	return nil
}
