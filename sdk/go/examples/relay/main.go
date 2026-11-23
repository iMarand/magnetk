// Embed a relay in your own program. Run two with -peers to federate them
// (e.g. one per VPS): nodes attached to either relay reach each other.
//
//	go run ./examples/relay -listen 0.0.0.0:4002 -secret s3cret
//	go run ./examples/relay -listen 0.0.0.0:4012 -peers 127.0.0.1:4002 -secret s3cret
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/iMarand/magnetk/sdk/go/magnetk"
)

func main() {
	listen := flag.String("listen", "0.0.0.0:4002", "QUIC+TCP listen address")
	peers := flag.String("peers", "", "other relays to federate with, comma separated")
	secret := flag.String("secret", "", "cluster secret (same on every relay)")
	flag.Parse()

	var peerList []string
	if *peers != "" {
		peerList = strings.Split(*peers, ",")
	}
	r, err := magnetk.StartRelay(magnetk.RelayConfig{
		Listen:        *listen,
		Peers:         peerList,
		ClusterSecret: *secret,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer r.Close()
	fmt.Printf("relay %s on %s (quic=%v)\n", r.ID(), r.Addr(), r.QUICEnabled())

	tick := time.NewTicker(10 * time.Second)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	for {
		select {
		case <-tick.C:
			s := r.Stats()
			fmt.Printf("nodes=%d remote=%d links=%v pipes=%d heap=%dMB\n", s.Nodes, s.RemoteNodes, s.Links, s.ActivePipes, s.Runtime.HeapMB)
		case <-sig:
			return
		}
	}
}
