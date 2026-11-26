// Secure chat: end-to-end encrypted, signed one-to-one messages between
// identities. Strangers arrive as message requests; compare the safety
// number once to be sure nobody is in the middle.
//
//	go run ./examples/securechat -relay 127.0.0.1:4002 -id alice -to bob
//	go run ./examples/securechat -relay 127.0.0.1:4002 -id bob -to alice
//
// Type a line to send it. Commands: /allow, /block, /safety, /verified.
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
	id := flag.String("id", "", "your node id")
	to := flag.String("to", "", "who to talk to (node id or magnetk:// address)")
	keyFile := flag.String("key", "", "identity key file (default <id>.key)")
	flag.Parse()
	if *id == "" || *to == "" {
		log.Fatal("usage: securechat -id <you> -to <them>")
	}
	if *keyFile == "" {
		*keyFile = *id + ".key"
	}
	key, err := magnetk.LoadOrCreateKey(*keyFile)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	n, err := magnetk.ConnectConfig(ctx, magnetk.Config{Relays: strings.Split(*relay, ","), ID: *id, Key: key})
	if err != nil {
		log.Fatal(err)
	}
	defer n.Close()

	peer := *to
	sc := magnetk.NewSecureChat(n, *id+"-files", *id+"-peers.json", magnetk.ChatPolicy{RequireApproval: true},
		func(m magnetk.ChatMessage) {
			switch {
			case m.Kind == "ack":
				fmt.Printf("\r  (delivered, %.1f ms)\n> ", m.RTTMs)
			case m.Error != "":
				fmt.Printf("\r!!! message from %s could not be read: %s\n> ", m.From, m.Error)
			default:
				lock := "encrypted"
				if !m.Encrypted {
					lock = "NOT encrypted"
				}
				fmt.Printf("\r%s: %s   [%s, key %s]\n", m.From, m.Text, lock, m.Trust)
				if m.Request {
					fmt.Printf("   message request: /allow to accept %s, /block to drop them\n", m.From)
				}
				fmt.Print("> ")
			}
		})
	if a, err := magnetk.ParseAddress(peer); err == nil {
		peer = a.Node
		if a.Key != "" {
			sc.Pins().Pin(peer, a.Key) // the address says which key must answer
		}
	}
	go func() {
		for m := range n.Messages() {
			sc.HandleMessage(m)
		}
	}()

	me := magnetk.Address{Node: n.ID(), Relays: []string{n.Info().Relay}, Key: key.Fingerprint()}
	fmt.Printf("you are %s\nyour address: %s\nchatting with %s\n> ", n.ID(), me.String(), peer)
	sc2 := bufio.NewScanner(os.Stdin)
	for sc2.Scan() {
		line := strings.TrimSpace(sc2.Text())
		switch line {
		case "":
		case "/allow":
			sc.Allow(peer)
		case "/block":
			sc.Block(peer)
		case "/safety":
			fmt.Println("safety number:", sc.SafetyNumber(peer), "- compare it with", peer)
		case "/verified":
			sc.MarkVerified(peer)
		default:
			if _, err := sc.Send(ctx, peer, line, "", 0, nil); err != nil {
				fmt.Println("send failed:", err)
			}
		}
		fmt.Print("> ")
	}
}
