// Command peer downloads a file (same as `magnetk get`, with Magnetk v1
// flags: -magnetk, -output, -relay, -transport, -relay-assisted).
package main

import (
	"os"

	"github.com/iMarand/magnetk/go/internal/cli"
)

func main() { os.Exit(cli.PeerCompat(os.Args[1:])) }
