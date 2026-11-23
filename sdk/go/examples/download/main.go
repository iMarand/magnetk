// Download a magnetk link (multi-source, verified, resumable).
//
//	go run ./examples/download "magnetk:?xt=..." [output]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/iMarand/magnetk/sdk/go/magnetk"
)

func main() {
	relay := flag.String("relay", "", "relay address (default: the relays in the link)")
	flag.Parse()
	if flag.NArg() < 1 {
		log.Fatal("usage: download [-relay addr] <link> [output]")
	}
	l, err := magnetk.ParseLink(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	relays := l.Relays
	if *relay != "" {
		relays = strings.Split(*relay, ",")
	}
	if len(relays) == 0 {
		log.Fatal("the link names no relay; pass -relay")
	}

	ctx := context.Background()
	n, err := magnetk.ConnectConfig(ctx, magnetk.Config{Relays: relays})
	if err != nil {
		log.Fatal(err)
	}
	defer n.Close()

	res, err := n.Download(ctx, flag.Arg(0), flag.Arg(1), &magnetk.DownloadOptions{
		Progress: func(p magnetk.Progress) {
			fmt.Printf("\r%5.1f%%  %6.1f MB/s  eta %-6v [%d source(s)]  ", p.Percent(), p.BytesPerSec/1e6, p.ETA.Round(time.Second), p.Sources)
		},
	})
	if err != nil {
		log.Fatal("\n", err)
	}
	fmt.Printf("\nsaved %s (%d bytes in %v, %d bytes resumed)\n", res.Path, res.Manifest.Size, res.Elapsed.Round(time.Millisecond), res.Resumed)
}
