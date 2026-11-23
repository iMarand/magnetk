package transport

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestCSourcesInSync fails when go/pkg/transport carries a stale copy of the
// C transport. Fix with: go generate ./pkg/transport
func TestCSourcesInSync(t *testing.T) {
	for orig, copyName := range map[string]string{
		filepath.Join("..", "..", "..", "c", "src", "transport.c"):     "transport_impl.c",
		filepath.Join("..", "..", "..", "c", "include", "transport.h"): "transport.h",
	} {
		want, err := os.ReadFile(orig)
		if err != nil {
			t.Skipf("original C sources not present (%v)", err)
		}
		got, err := os.ReadFile(copyName)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasSuffix(got, want) {
			t.Fatalf("%s is out of date with %s: run `go generate ./pkg/transport`", copyName, orig)
		}
	}
}
