package main

import (
	"fmt"
	"log"
	"magnetk/sdk/go/magnetk"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <magnetk_link>")
		return
	}
	magnetLink := os.Args[1]

	// Initialize Client
	client, err := magnetk.NewClient(magnetk.ClientConfig{
		DHTEnabled: true, // Enable DHT for trackerless discovery
	})
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	outputDir := "./downloads/"
	os.MkdirAll(outputDir, 0755)

	fmt.Printf("Downloading %s...\n", magnetLink)

	// Download
	err = client.Download(magnetLink, outputDir, func(p magnetk.Progress) {
		fmt.Printf("\rProgress: %.1f%% | Speed: %.1f MB/s", p.Percent, p.SpeedMBps)
	})
	fmt.Println()

	if err != nil {
		log.Fatalf("Download failed: %v", err)
	}

	fmt.Println("Download complete!")
}
