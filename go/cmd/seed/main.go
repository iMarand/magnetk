// Command seed shares files (same as `magnetk share`, with Magnetk v1 flags:
// -file, -relay, -port, -relay-assisted, -holepunch).
package main

import (
	"os"

	"github.com/iMarand/magnetk/go/internal/cli"
)

func main() { os.Exit(cli.SeedCompat(os.Args[1:])) }
