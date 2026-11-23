// Pipes: reliable byte streams between nodes, peer-to-peer when possible.
//
//	go run ./examples/pipe -mode server -id sink
//	go run ./examples/pipe -mode client -to sink -mb 200 -route auto
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/iMarand/magnetk/sdk/go/magnetk"
)

func main() {
	relay := flag.String("relay", "127.0.0.1:4002", "relay address")
	mode := flag.String("mode", "client", "server or client")
	id := flag.String("id", "", "node id")
	to := flag.String("to", "sink", "server node id (client mode)")
	mb := flag.Int("mb", 100, "megabytes to send")
	route := flag.String("route", "auto", "auto, direct or relay")
	flag.Parse()

	ctx := context.Background()
	n, err := magnetk.ConnectConfig(ctx, magnetk.Config{Relays: []string{*relay}, ID: *id, Listen: true})
	if err != nil {
		log.Fatal(err)
	}
	defer n.Close()

	if *mode == "server" {
		n.HandlePipe("sink", func(p *magnetk.Pipe) {
			defer p.Close()
			t0 := time.Now()
			nb, _ := io.Copy(io.Discard, p)
			el := time.Since(t0)
			fmt.Printf("%s: %d bytes in %v (%.1f MB/s, direct=%v)\n", p.Peer, nb, el.Round(time.Millisecond), float64(nb)/el.Seconds()/1e6, p.Direct)
			p.Write([]byte("ok"))
		})
		fmt.Println("sink ready as", n.ID())
		select {}
	}

	p, err := n.OpenPipe(ctx, *to, "sink", magnetk.WithRoute(magnetk.Route(*route)))
	if err != nil {
		log.Fatal(err)
	}
	defer p.Close()
	buf := make([]byte, 1<<20)
	t0 := time.Now()
	for i := 0; i < *mb; i++ {
		if _, err := p.Write(buf); err != nil {
			log.Fatal(err)
		}
	}
	p.CloseWrite()
	io.ReadAll(p) // wait for the acknowledgement
	el := time.Since(t0)
	fmt.Printf("sent %d MiB in %v: %.1f MB/s (direct=%v)\n", *mb, el.Round(time.Millisecond), float64(*mb)*1.048576/el.Seconds(), p.Direct)
}
