// Command relay runs a Magnetk relay (same as `magnetk relay`, also accepts
// the Magnetk v1 flags).
package main

import (
	"os"

	"github.com/iMarand/magnetk/go/internal/cli"
)

func main() { os.Exit(cli.RelayCompat(os.Args[1:])) }
