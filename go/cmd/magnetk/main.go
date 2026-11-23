// Command magnetk is the Magnetk command-line tool: relay server, file
// sharing, messaging and benchmarks. Run `magnetk help`.
package main

import (
	"os"

	"github.com/iMarand/magnetk/go/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:])) }
