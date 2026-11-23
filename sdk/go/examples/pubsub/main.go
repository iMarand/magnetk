// Publish/subscribe throughput demo with a queue group: each message is
// delivered to exactly one of the workers.
//
//	go run ./examples/pubsub -relay 127.0.0.1:4002 -n 100000
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/iMarand/magnetk/sdk/go/magnetk"
)

func main() {
	relay := flag.String("relay", "127.0.0.1:4002", "relay address")
	count := flag.Int("n", 100000, "messages to publish")
	size := flag.Int("size", 128, "payload bytes")
	workers := flag.Int("workers", 2, "queue-group workers")
	flag.Parse()

	ctx := context.Background()
	var got atomic.Int64
	done := make(chan struct{})
	for i := 0; i < *workers; i++ {
		w, err := magnetk.Connect(ctx, *relay)
		if err != nil {
			log.Fatal(err)
		}
		defer w.Close()
		if _, err := w.QueueSubscribe(ctx, "jobs.*", "workers", func(m *magnetk.Message) {
			if got.Add(1) == int64(*count) {
				close(done)
			}
		}); err != nil {
			log.Fatal(err)
		}
	}

	pub, err := magnetk.Connect(ctx, *relay)
	if err != nil {
		log.Fatal(err)
	}
	defer pub.Close()

	payload := make([]byte, *size)
	start := time.Now()
	for i := 0; i < *count; i++ {
		if err := pub.Publish(ctx, "jobs.resize", payload); err != nil {
			log.Fatal(err)
		}
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		fmt.Println("timeout")
	}
	el := time.Since(start)
	n := got.Load()
	fmt.Printf("%d/%d messages in %v: %.0f msg/s, %.1f MB/s\n", n, *count, el.Round(time.Millisecond),
		float64(n)/el.Seconds(), float64(n)*float64(*size)/el.Seconds()/1e6)
}
