// Request/reply service. Run several servers: requests are load-balanced.
//
//	go run ./examples/rpc -mode server
//	go run ./examples/rpc -mode client
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
	relay := flag.String("relay", "127.0.0.1:4002", "relay address")
	mode := flag.String("mode", "client", "server or client")
	flag.Parse()

	ctx := context.Background()
	n, err := magnetk.Connect(ctx, *relay)
	if err != nil {
		log.Fatal(err)
	}
	defer n.Close()

	if *mode == "server" {
		if _, err := n.HandleRequests(ctx, "svc.upper", func(m *magnetk.Message) ([]byte, error) {
			return []byte(strings.ToUpper(string(m.Data)) + " (by " + n.ID() + ")"), nil
		}); err != nil {
			log.Fatal(err)
		}
		fmt.Println("serving svc.upper as", n.ID())
		select {}
	}
	for i := 0; i < 5; i++ {
		t0 := time.Now()
		r, err := n.RequestTopic(ctx, "svc.upper", []byte(fmt.Sprintf("hello %d", i)))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s  (%v)\n", r, time.Since(t0).Round(time.Microsecond))
	}
}
