package main

import (
	"fmt"
	"log"
	"magnetk/sdk/go/magnetk"
)

func main() {
	// Initialize Seed
	// explicit DHT configuration for robustness
	seed, err := magnetk.NewSeed(magnetk.SeedConfig{
		RelayAddr:  "69.169.109.243:4003", // Example Relay
		DHTEnabled: true,                  // Enable DHT discovery
		// DHTBootstrapPeers: []string{"..."}, // Optional custom bootstrap peers
	})
	if err != nil {
		log.Fatalf("Failed to start seed: %v", err)
	}
	defer seed.Close()

	// Publish File
	filePath := "./test_file.txt" // Ensure this file exists
	fmt.Printf("Sharing %s...\n", filePath)

	link, err := seed.PublishFile(filePath)
	if err != nil {
		log.Fatalf("Failed to publish file: %v", err)
	}

	fmt.Println("--------------------------------------------------")
	fmt.Println("FILE SHARED SUCCESSFULLY!")
	fmt.Println("Magnetk Link:", link)
	fmt.Println("--------------------------------------------------")

	// Keep running to serve the file
	fmt.Println("Press Ctrl+C to stop seeding...")
	select {}
}
