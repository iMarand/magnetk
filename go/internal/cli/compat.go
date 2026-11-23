package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/iMarand/magnetk/go/pkg/magnetk"
)

// The relay/seed/peer binaries keep their Magnetk v1 flags so existing
// scripts (and the JS SDK, which runs the seed binary) keep working.

// RelayCompat runs `relay` with v1 flags (-port, -bind, -config, ...).
func RelayCompat(args []string) int {
	// v1 flags that no longer apply are accepted and ignored.
	var out []string
	for i := 0; i < len(args); i++ {
		a := strings.TrimLeft(args[i], "-")
		switch {
		case strings.HasPrefix(a, "discovery-port"):
			if !strings.Contains(a, "=") && i+1 < len(args) {
				i++
			}
			fmt.Fprintln(os.Stderr, "note: -discovery-port is obsolete (v2 uses one port for QUIC and TCP)")
		case a == "dht" || strings.HasPrefix(a, "dht="):
			fmt.Fprintln(os.Stderr, "note: -dht is not part of the v2 relay; use federation (-peers) instead")
		default:
			out = append(out, args[i])
		}
	}
	if err := RelayMain(out); err != nil {
		fmt.Fprintln(os.Stderr, "relay:", err)
		return 1
	}
	return 0
}

type legacyNodeFlags struct {
	nf            *nodeFlags
	relayAssisted *string
	relayAddrs    *string
	holePunch     *bool
	dht           *bool
	discovery     *string
	bootstrap     *string
}

func newLegacyNodeFlags(name string) *legacyNodeFlags {
	nf := newNodeFlags(name)
	return &legacyNodeFlags{
		nf:            nf,
		relayAssisted: nf.fs.String("relay-assisted", "", "v1: auto | true (always relay) | false (direct only)"),
		relayAddrs:    nf.fs.String("relay-addresses", "", "v1: additional relays (comma separated)"),
		holePunch:     nf.fs.Bool("holepunch", false, "v1: enable UPnP port mapping"),
		dht:           nf.fs.Bool("dht", false, "v1: ignored in v2"),
		discovery:     nf.fs.String("discovery", "", "v1: ignored in v2"),
		bootstrap:     nf.fs.String("bootstrap", "", "v1: ignored in v2"),
	}
}

// apply maps v1 options onto v2 flags.
func (l *legacyNodeFlags) apply() {
	nf := l.nf
	relays := splitList(*nf.relays)
	relays = append(relays, splitList(*l.relayAddrs)...)
	for i, r := range relays {
		relays[i] = magnetk.NormalizeRelay(r)
	}
	*nf.relays = strings.Join(relays, ",")
	switch strings.ToLower(*l.relayAssisted) {
	case "true":
		*nf.route = "relay"
	case "false":
		*nf.route = "direct"
	}
	if *l.dht || *l.discovery != "" || *l.bootstrap != "" {
		fmt.Fprintln(os.Stderr, "note: DHT discovery is not part of Magnetk v2; relays (optionally federated) are used instead")
	}
}

// SeedCompat runs `seed -file F -relay R [-port P]`.
func SeedCompat(args []string) int {
	l := newLegacyNodeFlags("seed")
	file := l.nf.fs.String("file", "", "file to share (required)")
	chunk := l.nf.fs.String("chunk", "", "chunk size (e.g. 1MB)")
	l.nf.linkRelays = l.nf.fs.String("link-relay", "", "public relay address(es) to put in links")
	if err := l.nf.parse(args); err != nil {
		return 2
	}
	files := l.nf.fs.Args()
	if *file != "" {
		files = append([]string{*file}, files...)
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "usage: seed -file <path> -relay <host:port> [-port <direct port>]")
		l.nf.fs.PrintDefaults()
		return 2
	}
	l.apply()
	if *l.holePunch {
		os.Setenv("MAGNETK_UPNP", "true")
	}
	n, err := l.nf.start(nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		return 1
	}
	defer n.Close()
	if err := shareAndServe(n, files, *chunk, false); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		return 1
	}
	return 0
}

// PeerCompat runs `peer -magnetk LINK [-output PATH]`.
func PeerCompat(args []string) int {
	l := newLegacyNodeFlags("peer")
	link := l.nf.fs.String("magnetk", "", "magnetk link to download (required)")
	output := l.nf.fs.String("output", "", "output file or directory")
	seed := l.nf.fs.Bool("seed", false, "keep sharing after the download")
	if err := l.nf.parse(args); err != nil {
		return 2
	}
	target := *link
	if target == "" && l.nf.fs.NArg() > 0 {
		target = l.nf.fs.Arg(0)
	}
	if target == "" {
		fmt.Fprintln(os.Stderr, "usage: peer -magnetk <link> [-output <path>]")
		l.nf.fs.PrintDefaults()
		return 2
	}
	l.apply()
	if err := download(l.nf, target, *output, *seed, 0, false); err != nil {
		fmt.Fprintln(os.Stderr, "peer:", err)
		return 1
	}
	return 0
}
