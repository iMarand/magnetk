// Share a file and serve it until interrupted.
//
//	go run ./examples/share -relay 127.0.0.1:4002 bigfile.iso
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"

	"github.com/iMarand/magnetk/sdk/go/magnetk"
)

func main() {
	relay := flag.String("relay", "127.0.0.1:4002", "relay address(es), comma separated")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: share [-relay addr] <file>")
	}
	ctx := context.Background()
	n, err := magnetk.ConnectConfig(ctx, magnetk.Config{Relays: strings.Split(*relay, ","), Listen: true})
	if err != nil {
		log.Fatal(err)
	}
	defer n.Close()

	f, err := n.Share(ctx, flag.Arg(0), &magnetk.ShareOptions{Progress: func(done, total int64) {
		if total > 0 {
			fmt.Printf("\rhashing %.0f%%", float64(done)*100/float64(total))
		}
	}})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\rSharing %s (%d bytes)\n", f.Name, f.Size)
	fmt.Println("Magnetk Link:", f.Link)
	fmt.Println("Serving; Ctrl+C to stop.")
	select {}
}
