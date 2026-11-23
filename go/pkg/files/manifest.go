// Package files implements Magnetk content distribution: manifests (file
// hash + per-chunk SHA-256), a store of shared files, the chunk server and a
// parallel, multi-source, resumable downloader that verifies every chunk.
package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/iMarand/magnetk/go/pkg/wire"
)

// Chunk size bounds.
const (
	MinChunkSize = 64 << 10
	MaxChunkSize = 16 << 20
)

// Manifest describes a file as an ordered list of chunk hashes.
type Manifest struct {
	Hash        string // hex SHA-256 of the whole file
	Name        string
	Size        int64
	ChunkSize   int64
	ChunkHashes [][32]byte
	Root        string // hex SHA-256 over the concatenated chunk hashes
}

// NumChunks returns the number of chunks.
func (m *Manifest) NumChunks() int { return len(m.ChunkHashes) }

// ChunkLen returns the length of chunk i.
func (m *Manifest) ChunkLen(i int) int64 {
	off := int64(i) * m.ChunkSize
	if off+m.ChunkSize > m.Size {
		return m.Size - off
	}
	return m.ChunkSize
}

// Wire converts the manifest to its protocol form.
func (m *Manifest) Wire() *wire.ManifestRes {
	return &wire.ManifestRes{Hash: m.Hash, Name: m.Name, Size: uint64(m.Size), ChunkSize: uint32(m.ChunkSize), ChunkHashes: m.ChunkHashes}
}

// FromWire validates a received manifest. expectRoot may be empty.
func FromWire(w *wire.ManifestRes, expectHash, expectRoot string) (*Manifest, error) {
	m := &Manifest{Hash: w.Hash, Name: filepath.Base(w.Name), Size: int64(w.Size), ChunkSize: int64(w.ChunkSize), ChunkHashes: w.ChunkHashes}
	if expectHash != "" && m.Hash != expectHash {
		return nil, fmt.Errorf("manifest is for %s, expected %s", short(m.Hash), short(expectHash))
	}
	if m.ChunkSize < 1 || m.ChunkSize > MaxChunkSize {
		return nil, fmt.Errorf("manifest chunk size %d out of range", m.ChunkSize)
	}
	want := int((m.Size + m.ChunkSize - 1) / m.ChunkSize)
	if m.Size == 0 {
		want = 0
	}
	if len(m.ChunkHashes) != want {
		return nil, fmt.Errorf("manifest has %d chunks, size implies %d", len(m.ChunkHashes), want)
	}
	m.Root = ComputeRoot(m.ChunkHashes)
	if expectRoot != "" && m.Root != expectRoot {
		return nil, fmt.Errorf("manifest root mismatch: the provider sent a manifest that does not match the link")
	}
	if m.Name == "." || m.Name == string(filepath.Separator) {
		m.Name = ""
	}
	return m, nil
}

// ComputeRoot hashes the concatenated chunk hashes.
func ComputeRoot(hashes [][32]byte) string {
	h := sha256.New()
	for i := range hashes {
		h.Write(hashes[i][:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// AutoChunkSize picks a chunk size: 1 MiB up to 2 GiB, then grows so a file
// has at most ~4096 chunks (bounded by MaxChunkSize).
func AutoChunkSize(size int64) int64 {
	cs := int64(1 << 20)
	for size/cs > 4096 && cs < MaxChunkSize {
		cs *= 2
	}
	return cs
}

// BuildManifest reads the file once: chunk hashes are computed in parallel
// on all cores while the whole-file hash runs over the same buffers in order.
// progress (may be nil) receives bytes hashed so far.
func BuildManifest(ctx context.Context, path string, chunkSize int64, progress func(done, total int64)) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	size := st.Size()
	if chunkSize <= 0 {
		chunkSize = AutoChunkSize(size)
	}
	if chunkSize < MinChunkSize {
		chunkSize = MinChunkSize
	}
	if chunkSize > MaxChunkSize {
		chunkSize = MaxChunkSize
	}
	n := int((size + chunkSize - 1) / chunkSize)
	m := &Manifest{Name: filepath.Base(path), Size: size, ChunkSize: chunkSize, ChunkHashes: make([][32]byte, n)}

	type block struct {
		idx  int
		buf  []byte
		refs atomic.Int32
	}
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	pool := make(chan []byte, workers+2)
	for i := 0; i < cap(pool); i++ {
		pool <- make([]byte, chunkSize)
	}
	release := func(b *block) {
		if b.refs.Add(-1) == 0 {
			pool <- b.buf[:cap(b.buf)]
		}
	}

	ordered := make(chan *block, cap(pool))
	hashJobs := make(chan *block, cap(pool))
	full := sha256.New()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // whole-file hash, in order
		defer wg.Done()
		for b := range ordered {
			full.Write(b.buf)
			release(b)
		}
	}()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range hashJobs {
				m.ChunkHashes[b.idx] = sha256.Sum256(b.buf)
				release(b)
			}
		}()
	}

	var readErr error
	var done int64
	for i := 0; i < n; i++ {
		if err := ctx.Err(); err != nil {
			readErr = err
			break
		}
		buf := <-pool
		want := chunkSize
		if int64(i)*chunkSize+want > size {
			want = size - int64(i)*chunkSize
		}
		if _, err := io.ReadFull(f, buf[:want]); err != nil {
			readErr = fmt.Errorf("read %s: %w", path, err)
			break
		}
		b := &block{idx: i, buf: buf[:want]}
		b.refs.Store(2)
		ordered <- b
		hashJobs <- b
		done += want
		if progress != nil {
			progress(done, size)
		}
	}
	close(ordered)
	close(hashJobs)
	wg.Wait()
	if readErr != nil {
		return nil, readErr
	}
	m.Hash = hex.EncodeToString(full.Sum(nil))
	m.Root = ComputeRoot(m.ChunkHashes)
	return m, nil
}

// VerifyFile recomputes the SHA-256 of a file.
func VerifyFile(path, expectHash string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != expectHash {
		return fmt.Errorf("file hash mismatch: got %s want %s", short(got), short(expectHash))
	}
	return nil
}

// ===== manifest cache =====

// CachedManifest returns a manifest for path, reusing a cached one when the
// file's size and modification time are unchanged. cacheDir "" disables it.
func CachedManifest(ctx context.Context, path, cacheDir string, chunkSize int64, progress func(done, total int64)) (*Manifest, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	var cachePath string
	if cacheDir != "" {
		key := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%d", abs, st.Size(), st.ModTime().UnixNano(), chunkSize)))
		cachePath = filepath.Join(cacheDir, hex.EncodeToString(key[:16])+".mkm")
		if data, err := os.ReadFile(cachePath); err == nil && len(data) > wire.HeaderSize {
			if w, err := wire.DecodeManifestRes(data[wire.HeaderSize:]); err == nil {
				if m, err := FromWire(&w, "", ""); err == nil {
					m.Name = filepath.Base(abs)
					return m, nil
				}
			}
		}
	}
	m, err := BuildManifest(ctx, abs, chunkSize, progress)
	if err != nil {
		return nil, err
	}
	if cachePath != "" {
		if os.MkdirAll(cacheDir, 0o755) == nil {
			tmp := cachePath + ".tmp"
			if os.WriteFile(tmp, m.Wire().Encode(), 0o644) == nil {
				os.Rename(tmp, cachePath)
			}
		}
	}
	return m, nil
}

// DefaultCacheDir is the per-user manifest cache location.
func DefaultCacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "magnetk", "manifests")
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// ErrNotFound means the requested file is not shared here.
var ErrNotFound = errors.New("files: not found")
