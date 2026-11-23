// Chat: everyone subscribed to chat.<room> sees every line typed.
//
//	go run ./examples/chat -relay 127.0.0.1:4002 -name alice -room lobby
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/iMarand/magnetk/sdk/go/magnetk"
)

func main() {
	relay := flag.String("relay", "127.0.0.1:4002", "relay address(es), comma separated")
	name := flag.String("name", "", "your name (node id)")
	room := flag.String("room", "lobby", "chat room")
	flag.Parse()

	ctx := context.Background()
	n, err := magnetk.ConnectConfig(ctx, magnetk.Config{Relays: strings.Split(*relay, ","), ID: *name, WatchPresence: true})
	if err != nil {
		log.Fatal(err)
	}
	defer n.Close()

	topic := "chat." + *room
	if _, err := n.Subscribe(ctx, topic, func(m *magnetk.Message) {
		if m.From != n.ID() {
			fmt.Printf("\r[%s] %s\n> ", m.From, m.Data)
		}
	}); err != nil {
		log.Fatal(err)
	}
	go func() { // presence events arrive in the inbox
		for m := range n.Messages() {
			if m.Kind == magnetk.KindPresence {
				fmt.Printf("\r* %s %s\n> ", m.From, m.Subject)
			}
		}
	}()

	fmt.Printf("joined %s as %s via %s\n> ", topic, n.ID(), n.Info().Relay)
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		if err := n.Publish(ctx, topic, sc.Bytes()); err != nil {
			fmt.Println("send failed:", err)
		}
		fmt.Print("> ")
	}
}
