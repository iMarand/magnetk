package files

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// pipeSrc serves a Store over loopback TCP (buffered, like real streams).
type pipeSrc struct {
	id    string
	store *Store
	ln    net.Listener
}

func (p *pipeSrc) ID() string { return p.id }

func (p *pipeSrc) Open(ctx context.Context) (io.ReadWriteCloser, error) {
	if p.ln == nil {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		p.ln = ln
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() { p.store.Serve(c); c.Close() }()
			}
		}()
	}
	return net.Dial("tcp", p.ln.Addr().String())
}

func share(t *testing.T, data []byte, chunk int64, noCompress bool) (*Store, *Manifest, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "src.bin")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := BuildManifest(context.Background(), path, chunk, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := NewStore()
	s.NoCompression = noCompress
	if err := s.Add(path, m); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close) // Windows cannot delete files that are still open
	return s, m, dir
}

func textData(n int) []byte {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "%08d,sensor-%d,temperature=%d.%d,status=ok\n", i, i%17, 20+i%9, i%10)
	}
	return []byte(b.String()[:n])
}

func TestCompressionAndRanges(t *testing.T) {
	ctx := context.Background()
	text := textData(5 << 20)
	random := make([]byte, 3<<20)
	rand.Read(random)

	cases := []struct {
		name       string
		data       []byte
		noCompress bool // provider side
		noAccept   bool // downloader side
		wantSmall  bool // wire bytes well below the size
	}{
		{"text compressed", text, false, false, true},
		{"text, downloader without compression", text, false, true, false},
		{"text, provider without compression", text, true, false, false},
		{"random data is sent as is", random, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, m, dir := share(t, c.data, 256<<10, c.noCompress)
			out := filepath.Join(dir, "out.bin")
			res, err := Download(ctx, []Source{&pipeSrc{id: "p1", store: s}}, Options{Hash: m.Hash, Root: m.Root, Out: out, NoCompression: c.noAccept})
			if err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(out)
			if !bytes.Equal(got, c.data) {
				t.Fatal("downloaded file differs")
			}
			ratio := float64(res.WireBytes) / float64(len(c.data))
			if c.wantSmall != (ratio < 0.6) {
				t.Fatalf("wire/size = %.2f (wire %d, size %d)", ratio, res.WireBytes, len(c.data))
			}
			t.Logf("wire/size = %.2f", ratio)
		})
	}

	t.Run("byte ranges", func(t *testing.T) {
		s, m, dir := share(t, text, 256<<10, false)
		src := []Source{&pipeSrc{id: "p1", store: s}}
		for _, r := range [][2]int64{{0, 100}, {1000, 300 << 10}, {256 << 10, 256 << 10}, {int64(len(text)) - 10, 0}, {3<<20 + 7, 1 << 20}} {
			out := filepath.Join(dir, fmt.Sprintf("range-%d-%d.bin", r[0], r[1]))
			res, err := Download(ctx, src, Options{Hash: m.Hash, Root: m.Root, Out: out, Offset: r[0], Length: r[1]})
			if err != nil {
				t.Fatalf("range %v: %v", r, err)
			}
			end := int64(len(text))
			if r[1] > 0 {
				end = r[0] + r[1]
			}
			got, _ := os.ReadFile(out)
			if !bytes.Equal(got, text[r[0]:end]) || res.Offset != r[0] {
				t.Fatalf("range %v: got %d bytes", r, len(got))
			}
			// only the needed chunks were transferred
			if need := (end-1)/m.ChunkSize - r[0]/m.ChunkSize + 1; res.Bytes > need*m.ChunkSize {
				t.Fatalf("range %v moved %d bytes for %d chunks", r, res.Bytes, need)
			}
		}
		if _, err := Download(ctx, src, Options{Hash: m.Hash, Out: filepath.Join(dir, "x"), Offset: 10}); err == nil {
			t.Fatal("range without root accepted")
		}
		if _, err := Download(ctx, src, Options{Hash: m.Hash, Root: m.Root, Out: filepath.Join(dir, "x"), Offset: int64(len(text))}); err == nil {
			t.Fatal("range past the end accepted")
		}
	})

	t.Run("adaptive window grows on a fast link", func(t *testing.T) {
		s, m, dir := share(t, random, 64<<10, false)
		var maxWin atomic.Int32
		_, err := Download(ctx, []Source{&pipeSrc{id: "p1", store: s}}, Options{Hash: m.Hash, Root: m.Root, Out: filepath.Join(dir, "a.bin"),
			StreamsPerSource: 1, ProgressInterval: 1, Progress: func(p Progress) {
				if int32(p.Window) > maxWin.Load() {
					maxWin.Store(int32(p.Window))
				}
			}})
		if err != nil {
			t.Fatal(err)
		}
		if maxWin.Load() <= 4 {
			t.Fatalf("window never grew (max %d)", maxWin.Load())
		}
		t.Logf("max window %d", maxWin.Load())
	})
}
