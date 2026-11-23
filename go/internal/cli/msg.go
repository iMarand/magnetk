package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/iMarand/magnetk/go/pkg/node"
)

func printMessage(m *node.Message) {
	ts := time.Now().Format("15:04:05.000")
	data := string(m.Data)
	switch m.Kind {
	case node.KindPresence:
		fmt.Printf("%s  presence  %-5s %s %s\n", ts, m.Subject, m.From, data)
	case node.KindPublish:
		fmt.Printf("%s  [%s] from %s: %s\n", ts, m.Subject, m.From, data)
	case node.KindRequest:
		fmt.Printf("%s  request from %s (%s): %s\n", ts, m.From, m.Subject, data)
	default:
		extra := ""
		if m.Datagram {
			extra = " (datagram)"
		}
		fmt.Printf("%s  message from %s%s: %s\n", ts, m.From, extra, data)
	}
}

// SendMain sends one direct message.
func SendMain(args []string) error {
	nf := newNodeFlags("send")
	receipt := nf.fs.Bool("receipt", true, "wait for the relay's delivery receipt")
	store := nf.fs.Bool("store", false, "store at the relay if the receiver is offline")
	unreliable := nf.fs.Bool("unreliable", false, "send as a QUIC datagram (no retransmission)")
	if err := nf.parse(args); err != nil {
		return err
	}
	if nf.fs.NArg() < 2 {
		return fmt.Errorf("usage: magnetk send [flags] <node> <message>")
	}
	n, err := nf.start(nil)
	if err != nil {
		return err
	}
	defer n.Close()
	var opts []node.SendOption
	if *receipt && !*unreliable {
		opts = append(opts, node.WithReceipt())
	}
	if *store {
		opts = append(opts, node.WithStore())
	}
	if *unreliable {
		opts = append(opts, node.Unreliable())
	}
	msg := strings.Join(nf.fs.Args()[1:], " ")
	if err := n.Send(context.Background(), nf.fs.Arg(0), []byte(msg), opts...); err != nil {
		return err
	}
	fmt.Println("sent")
	return nil
}

// PubMain publishes one message.
func PubMain(args []string) error {
	nf := newNodeFlags("pub")
	count := nf.fs.Int("n", 1, "number of times to publish")
	if err := nf.parse(args); err != nil {
		return err
	}
	if nf.fs.NArg() < 2 {
		return fmt.Errorf("usage: magnetk pub [flags] <topic> <message>")
	}
	n, err := nf.start(nil)
	if err != nil {
		return err
	}
	defer n.Close()
	msg := []byte(strings.Join(nf.fs.Args()[1:], " "))
	for i := 0; i < *count; i++ {
		if err := n.Publish(context.Background(), nf.fs.Arg(0), msg); err != nil {
			return err
		}
	}
	// Make sure the frames leave before we disconnect.
	if _, err := n.Ping(context.Background()); err != nil {
		return err
	}
	fmt.Printf("published %d message(s)\n", *count)
	return nil
}

// SubMain prints publications for a pattern until interrupted.
func SubMain(args []string) error {
	nf := newNodeFlags("sub")
	queue := nf.fs.String("queue", "", "join a queue group (each message goes to one member)")
	if err := nf.parse(args); err != nil {
		return err
	}
	if nf.fs.NArg() != 1 {
		return fmt.Errorf("usage: magnetk sub [flags] <pattern>")
	}
	n, err := nf.start(nil)
	if err != nil {
		return err
	}
	defer n.Close()
	ctx, cancel := signalContext()
	defer cancel()
	if _, err := n.QueueSubscribe(ctx, nf.fs.Arg(0), *queue, printMessage); err != nil {
		return err
	}
	fmt.Printf("Subscribed to %s as %s. Ctrl+C to stop.\n", nf.fs.Arg(0), n.ID())
	<-ctx.Done()
	return nil
}

// ReqMain sends a request to a node or (with @topic) a service.
func ReqMain(args []string) error {
	nf := newNodeFlags("req")
	if err := nf.parse(args); err != nil {
		return err
	}
	if nf.fs.NArg() < 2 {
		return fmt.Errorf("usage: magnetk req [flags] <node|@topic> <message>")
	}
	n, err := nf.start(nil)
	if err != nil {
		return err
	}
	defer n.Close()
	target := nf.fs.Arg(0)
	msg := []byte(strings.Join(nf.fs.Args()[1:], " "))
	start := time.Now()
	var resp []byte
	if strings.HasPrefix(target, "@") {
		resp, err = n.RequestTopic(context.Background(), target[1:], msg)
	} else {
		resp, err = n.Request(context.Background(), target, msg)
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s\n(%v)\n", resp, time.Since(start).Round(time.Microsecond))
	return nil
}

// ServeMain answers requests on a topic by echoing them.
func ServeMain(args []string) error {
	nf := newNodeFlags("serve")
	prefix := nf.fs.String("prefix", "echo: ", "text prepended to each answer")
	if err := nf.parse(args); err != nil {
		return err
	}
	if nf.fs.NArg() != 1 {
		return fmt.Errorf("usage: magnetk serve [flags] <topic>")
	}
	n, err := nf.start(nil)
	if err != nil {
		return err
	}
	defer n.Close()
	ctx, cancel := signalContext()
	defer cancel()
	handler := func(m *node.Message) ([]byte, error) {
		printMessage(m)
		return append([]byte(*prefix), m.Data...), nil
	}
	if _, err := n.HandleRequests(ctx, nf.fs.Arg(0), handler); err != nil {
		return err
	}
	n.OnRequest(handler)
	fmt.Printf("Serving %s (and direct requests to %s). Ctrl+C to stop.\n", nf.fs.Arg(0), n.ID())
	<-ctx.Done()
	return nil
}

// ListenMain prints direct messages and presence events.
func ListenMain(args []string) error {
	nf := newNodeFlags("listen")
	presence := nf.fs.Bool("presence", true, "also print nodes joining and leaving")
	if err := nf.parse(args); err != nil {
		return err
	}
	cfg, err := nf.nodeConfig(nil)
	if err != nil {
		return err
	}
	cfg.WatchPresence = *presence
	n, err := node.New(cfg)
	if err != nil {
		return err
	}
	defer n.Close()
	ctx, cancel := signalContext()
	defer cancel()
	sctx, scancel := context.WithTimeout(ctx, *nf.timeout)
	err = n.Start(sctx)
	scancel()
	if err != nil {
		return err
	}
	n.OnRequest(func(m *node.Message) ([]byte, error) {
		printMessage(m)
		return []byte("ok"), nil
	})
	fmt.Printf("Listening as %s on relay %s. Ctrl+C to stop.\n", n.ID(), n.Info().RelayID)
	for {
		m, err := n.Receive(ctx)
		if err != nil {
			return nil
		}
		printMessage(m)
	}
}

// NodesMain lists connected nodes.
func NodesMain(args []string) error {
	nf := newNodeFlags("nodes")
	if err := nf.parse(args); err != nil {
		return err
	}
	n, err := nf.start(nil)
	if err != nil {
		return err
	}
	defer n.Close()
	list, err := n.ListNodes(context.Background(), nf.fs.Arg(0))
	if err != nil {
		return err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].NodeID < list[j].NodeID })
	fmt.Printf("%-28s %-18s %-22s %s\n", "NODE", "RELAY", "OBSERVED", "DIRECT")
	for _, ni := range list {
		fmt.Printf("%-28s %-18s %-22s %s\n", ni.NodeID, ni.Relay, ni.Observed, strings.Join(ni.Addrs, ","))
	}
	fmt.Printf("%d node(s)\n", len(list))
	return nil
}

// PingMain measures relay round trips.
func PingMain(args []string) error {
	nf := newNodeFlags("ping")
	count := nf.fs.Int("n", 10, "number of pings")
	if err := nf.parse(args); err != nil {
		return err
	}
	n, err := nf.start(nil)
	if err != nil {
		return err
	}
	defer n.Close()
	info := n.Info()
	fmt.Printf("Pinging relay %s (%s) via %s\n", info.RelayID, info.Relay, info.Protocol)
	var all []time.Duration
	for i := 0; i < *count; i++ {
		d, err := n.Ping(context.Background())
		if err != nil {
			return err
		}
		all = append(all, d)
		fmt.Printf("  %d: %v\n", i+1, d.Round(time.Microsecond))
		time.Sleep(100 * time.Millisecond)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	fmt.Printf("min %v  median %v  max %v\n", all[0].Round(time.Microsecond), all[len(all)/2].Round(time.Microsecond), all[len(all)-1].Round(time.Microsecond))
	return nil
}

// StatsMain prints relay statistics.
func StatsMain(args []string) error {
	nf := newNodeFlags("stats")
	if err := nf.parse(args); err != nil {
		return err
	}
	n, err := nf.start(nil)
	if err != nil {
		return err
	}
	defer n.Close()
	js, err := n.RelayStats(context.Background())
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if json.Indent(&out, js, "", "  ") == nil {
		out.WriteTo(os.Stdout)
		fmt.Println()
	} else {
		os.Stdout.Write(js)
	}
	return nil
}
