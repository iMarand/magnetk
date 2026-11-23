package node

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iMarand/magnetk/go/pkg/files"
	"github.com/iMarand/magnetk/go/pkg/magnetk"
	"github.com/iMarand/magnetk/go/pkg/wire"
)

// SharedFile describes a file this node shares.
type SharedFile struct {
	Hash      string
	Name      string
	Size      int64
	ChunkSize int64
	Root      string
	Path      string
	Link      string
}

// ShareOptions configures Share.
type ShareOptions struct {
	ChunkSize int64                   // 0 = node default / automatic
	Progress  func(done, total int64) // hashing progress
}

// Share hashes a file (once; results are cached), serves it to other nodes
// and registers it with the relay. The returned link can be given to anyone.
func (n *Node) Share(ctx context.Context, path string, opts *ShareOptions) (*SharedFile, error) {
	if opts == nil {
		opts = &ShareOptions{}
	}
	cs := opts.ChunkSize
	if cs == 0 {
		cs = n.cfg.ChunkSize
	}
	cacheDir := n.cfg.CacheDir
	switch cacheDir {
	case "":
		cacheDir = files.DefaultCacheDir()
	case "-":
		cacheDir = ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	m, err := files.CachedManifest(ctx, abs, cacheDir, cs, opts.Progress)
	if err != nil {
		return nil, err
	}
	if err := n.store.Add(abs, m); err != nil {
		return nil, err
	}
	n.publish(ctx, m)
	return n.sharedFile(m, abs), nil
}

// ShareManifest shares a file whose manifest is already known (e.g. right
// after downloading it), skipping the hashing pass.
func (n *Node) ShareManifest(ctx context.Context, path string, m *files.Manifest) (*SharedFile, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := n.store.Add(abs, m); err != nil {
		return nil, err
	}
	n.publish(ctx, m)
	return n.sharedFile(m, abs), nil
}

func (n *Node) publish(ctx context.Context, m *files.Manifest) {
	// If disconnected, attach() publishes all shared files on reconnect.
	n.mu.RLock()
	rc := n.cur
	n.mu.RUnlock()
	if rc != nil && rc.w.Send(ctx, n.publishFrame(m)) == nil {
		n.Flush(ctx) // registered at the relay when Share returns
	}
}

func (n *Node) publishFrame(m *files.Manifest) []byte {
	fp := wire.FilePublish{Hash: m.Hash, Name: m.Name, Size: uint64(m.Size), ChunkSize: uint32(m.ChunkSize), Root: m.Root}
	return fp.Encode()
}

func (n *Node) sharedFile(m *files.Manifest, path string) *SharedFile {
	relays := n.cfg.LinkRelays
	if len(relays) == 0 {
		relays = n.cfg.Relays
	}
	ml := &magnetk.MagnetLink{FileHash: m.Hash, FileName: m.Name, FileSize: m.Size, ChunkSize: m.ChunkSize,
		ManifestRoot: m.Root, SeedID: n.ID(), Relays: relays}
	return &SharedFile{Hash: m.Hash, Name: m.Name, Size: m.Size, ChunkSize: m.ChunkSize, Root: m.Root, Path: path, Link: ml.String()}
}

// Unshare stops sharing a file.
func (n *Node) Unshare(ctx context.Context, hash string) error {
	if !n.store.Remove(hash) {
		return fmt.Errorf("magnetk: %s is not shared", hash)
	}
	fu := wire.FileUnpublish{Hash: hash}
	if _, err := n.send(ctx, fu.Encode()); err == nil {
		n.Flush(ctx)
	}
	return nil
}

// SharedFiles lists the files this node shares.
func (n *Node) SharedFiles() []*SharedFile {
	var out []*SharedFile
	for _, m := range n.store.List() {
		_, path, _ := n.store.Get(m.Hash)
		out = append(out, n.sharedFile(m, path))
	}
	return out
}

// Providers is the relay's answer to a file lookup.
type Providers struct {
	Hash      string
	Name      string
	Size      int64
	ChunkSize int64
	Root      string
	Nodes     []wire.NodeInfo
}

// FindProviders asks the relay which nodes provide a file.
func (n *Node) FindProviders(ctx context.Context, hash string) (*Providers, error) {
	id := n.nextID.Add(1)
	ff := wire.FileFind{ID: id, Hash: strings.ToLower(hash)}
	q, err := n.query(ctx, id, ff.Encode())
	if err != nil {
		return nil, err
	}
	fp, err := wire.DecodeFileProviders(q.body)
	if err != nil {
		return nil, err
	}
	return &Providers{Hash: fp.Hash, Name: fp.Name, Size: int64(fp.Size), ChunkSize: int64(fp.ChunkSize), Root: fp.Root, Nodes: fp.Providers}, nil
}

// DownloadOptions configures Download.
type DownloadOptions struct {
	Progress         func(files.Progress)
	StreamsPerSource int  // default from Config
	Pipeline         int  // outstanding chunk requests per stream (default 4)
	MaxSources       int  // default 8
	NoResume         bool // ignore/keep no partial state
	Seed             bool // keep sharing the file after the download
	Route            Route
}

// Download fetches the file described by a magnetk link (or a bare 64-char
// hash). out may be a file path, a directory, or "" (current directory).
func (n *Node) Download(ctx context.Context, linkOrHash, out string, opts *DownloadOptions) (*files.Result, error) {
	if opts == nil {
		opts = &DownloadOptions{}
	}
	var ml *magnetk.MagnetLink
	if strings.HasPrefix(linkOrHash, "magnetk:") {
		var err error
		if ml, err = magnetk.Parse(linkOrHash); err != nil {
			return nil, err
		}
	} else {
		ml = &magnetk.MagnetLink{FileHash: strings.ToLower(strings.TrimSpace(linkOrHash))}
		if err := ml.Validate(); err != nil {
			return nil, err
		}
	}

	prov, err := n.FindProviders(ctx, ml.FileHash)
	if err != nil {
		return nil, err
	}
	self := n.ID()
	var sources []files.Source
	for _, p := range prov.Nodes {
		if p.NodeID != self {
			sources = append(sources, &pipeSource{n: n, peer: p.NodeID, route: opts.Route})
		}
	}
	if len(sources) == 0 {
		if _, path, ok := n.store.Get(ml.FileHash); ok {
			return nil, fmt.Errorf("magnetk: this node already shares the file at %s", path)
		}
		return nil, ErrNoProviders
	}

	name := ml.FileName
	if name == "" {
		name = prov.Name
	}
	if name == "" {
		name = ml.FileHash[:16]
	}
	name = filepath.Base(name)
	outPath := out
	if outPath == "" {
		outPath = name
	} else if st, err := os.Stat(outPath); (err == nil && st.IsDir()) || strings.HasSuffix(outPath, "/") || strings.HasSuffix(outPath, string(os.PathSeparator)) {
		outPath = filepath.Join(outPath, name)
	}

	root := ml.ManifestRoot
	streams := opts.StreamsPerSource
	if streams == 0 {
		streams = n.cfg.StreamsPerSource
	}
	res, err := files.Download(ctx, sources, files.Options{
		Hash:             ml.FileHash,
		Root:             root,
		Out:              outPath,
		StreamsPerSource: streams,
		Pipeline:         opts.Pipeline,
		MaxSources:       opts.MaxSources,
		Resume:           !opts.NoResume,
		Progress:         opts.Progress,
		Logger:           n.log,
	})
	if err != nil {
		return nil, err
	}
	if opts.Seed {
		if _, err := n.ShareManifest(ctx, res.Path, res.Manifest); err != nil {
			n.log.Printf("[node] seeding %s failed: %v", res.Path, err)
		}
	}
	return res, nil
}

// pipeSource opens "mk.file" pipes to one provider.
type pipeSource struct {
	n     *Node
	peer  string
	route Route
}

func (s *pipeSource) ID() string { return s.peer }

func (s *pipeSource) Open(ctx context.Context) (io.ReadWriteCloser, error) {
	var opts []PipeOption
	if s.route != "" {
		opts = append(opts, WithRoute(s.route))
	}
	p, err := s.n.OpenPipe(ctx, s.peer, ServiceFile, opts...)
	if err != nil {
		return nil, err
	}
	return p, nil
}
