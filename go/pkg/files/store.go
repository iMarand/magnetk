package files

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/iMarand/magnetk/go/pkg/protocol"
	"github.com/iMarand/magnetk/go/pkg/wire"
)

// Store holds the files this node shares.
type Store struct {
	mu    sync.RWMutex
	files map[string]*shared

	pools sync.Map // chunk size -> *sync.Pool of []byte

	ServedChunks atomic.Uint64
	ServedBytes  atomic.Uint64 // chunk bytes served (uncompressed)
	WireBytes    atomic.Uint64 // bytes actually sent (after compression)

	// NoCompression disables on-the-fly chunk compression for new shares.
	NoCompression bool
}

type shared struct {
	m        *Manifest
	path     string
	f        *os.File
	compress bool // content looked compressible when it was shared
}

// NewStore creates an empty store.
func NewStore() *Store { return &Store{files: make(map[string]*shared)} }

// Add starts sharing path under manifest m.
func (s *Store) Add(path string, m *Manifest) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	if st.Size() != m.Size {
		f.Close()
		return fmt.Errorf("%s changed size since it was hashed", path)
	}
	compress := !s.NoCompression && isCompressible(f, m.Size)
	s.mu.Lock()
	if old := s.files[m.Hash]; old != nil {
		old.f.Close()
	}
	s.files[m.Hash] = &shared{m: m, path: path, f: f, compress: compress}
	s.mu.Unlock()
	return nil
}

// Remove stops sharing a file.
func (s *Store) Remove(hash string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh := s.files[hash]
	if sh == nil {
		return false
	}
	sh.f.Close()
	delete(s.files, hash)
	return true
}

// Get returns the manifest of a shared file.
func (s *Store) Get(hash string) (*Manifest, string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sh := s.files[hash]
	if sh == nil {
		return nil, "", false
	}
	return sh.m, sh.path, true
}

// List returns the manifests of all shared files.
func (s *Store) List() []*Manifest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Manifest, 0, len(s.files))
	for _, sh := range s.files {
		out = append(out, sh.m)
	}
	return out
}

// Close stops sharing everything.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, sh := range s.files {
		sh.f.Close()
		delete(s.files, h)
	}
}

func (s *Store) lookup(hash string) *shared {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.files[hash]
}

func (s *Store) buffer(size int) *[]byte {
	p, _ := s.pools.LoadOrStore(size, &sync.Pool{New: func() any { b := make([]byte, size); return &b }})
	return p.(*sync.Pool).Get().(*[]byte)
}

func (s *Store) putBuffer(size int, b *[]byte) {
	if p, ok := s.pools.Load(size); ok {
		p.(*sync.Pool).Put(b)
	}
}

// writeChunk sends one chunk frame with a single write, compressed when the
// requester accepts it and it saves enough.
func (s *Store) writeChunk(w io.Writer, sh *shared, idx uint32, acceptZ bool) error {
	m := sh.m
	if int(idx) >= m.NumChunks() {
		fe := wire.FileError{Code: wire.ErrCodeNotFound, Index: idx, Msg: "chunk index out of range"}
		_, err := w.Write(fe.Encode())
		return err
	}
	n := int(m.ChunkLen(int(idx)))
	bufSize := wire.HeaderSize + 4 + int(m.ChunkSize)
	bp := s.buffer(bufSize)
	defer s.putBuffer(bufSize, bp)
	buf := (*bp)[:wire.HeaderSize+4+n]
	copy(buf, wire.ChunkDataHeader(idx, n))
	if _, err := sh.f.ReadAt(buf[wire.HeaderSize+4:], int64(idx)*m.ChunkSize); err != nil && err != io.EOF {
		fe := wire.FileError{Code: wire.ErrCodeInternal, Index: idx, Msg: "read failed"}
		_, werr := w.Write(fe.Encode())
		if werr != nil {
			return werr
		}
		return nil
	}
	if acceptZ && sh.compress {
		var z bytes.Buffer
		z.Grow(n / 2)
		raw := buf[wire.HeaderSize+4:]
		if deflateTo(&z, raw) == nil && float64(z.Len()) < chunkMaxRatio*float64(n) {
			if _, err := w.Write(append(wire.ChunkDataZHeader(idx, n, z.Len()), z.Bytes()...)); err != nil {
				return err
			}
			s.ServedChunks.Add(1)
			s.ServedBytes.Add(uint64(n))
			s.WireBytes.Add(uint64(wire.HeaderSize + 8 + z.Len()))
			return nil
		}
	}
	if _, err := w.Write(buf); err != nil {
		return err
	}
	s.ServedChunks.Add(1)
	s.ServedBytes.Add(uint64(n))
	s.WireBytes.Add(uint64(len(buf)))
	return nil
}

// Serve answers v2 file requests (MANIFEST_REQ / CHUNK_REQ) on rw until EOF.
func (s *Store) Serve(rw io.ReadWriter) error {
	r := bufio.NewReaderSize(rw, 16<<10)
	for {
		typ, body, err := wire.ReadFrame(r, 1<<20)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		switch typ {
		case wire.TManifestReq:
			hash, err := wire.DecodeManifestReq(body)
			if err != nil {
				return err
			}
			sh := s.lookup(hash)
			if sh == nil {
				fe := wire.FileError{Code: wire.ErrCodeNotFound, Msg: "file not shared here"}
				if _, err := rw.Write(fe.Encode()); err != nil {
					return err
				}
				continue
			}
			if _, err := rw.Write(sh.m.Wire().Encode()); err != nil {
				return err
			}
		case wire.TChunkReq:
			req, err := wire.DecodeChunkReq(body)
			if err != nil {
				return err
			}
			sh := s.lookup(req.Hash)
			if sh == nil {
				fe := wire.FileError{Code: wire.ErrCodeNotFound, Msg: "file not shared here"}
				if _, err := rw.Write(fe.Encode()); err != nil {
					return err
				}
				continue
			}
			acceptZ := req.Flags&wire.ChunkAcceptCompressed != 0
			for _, idx := range req.Indices {
				if err := s.writeChunk(rw, sh, idx, acceptZ); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("files: unexpected frame 0x%02x", typ)
		}
	}
}

// ServeV1 answers Magnetk v1 (JSON) file requests, used by the JS SDK.
func (s *Store) ServeV1(rw io.ReadWriter) error {
	r := protocol.NewFrameReader(rw)
	w := protocol.NewFrameWriter(rw)
	for {
		f, err := r.ReadFrame()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		switch f.Type {
		case protocol.MsgManifestReq:
			var req protocol.ManifestRequestPayload
			if json.Unmarshal(f.Payload, &req) != nil {
				return fmt.Errorf("invalid v1 manifest request")
			}
			sh := s.lookup(req.FileHash)
			if sh == nil {
				w.WriteJSON(protocol.MsgError, protocol.ErrorPayload{Code: 404, Message: "file not found"})
				continue
			}
			m := sh.m
			res := protocol.ManifestResponsePayload{FileHash: m.Hash, FileName: m.Name, FileSize: m.Size, ChunkSize: m.ChunkSize, TotalChunks: uint32(m.NumChunks())}
			res.Chunks = make([]protocol.ChunkMeta, m.NumChunks())
			for i := range res.Chunks {
				res.Chunks[i] = protocol.ChunkMeta{Index: uint32(i), Hash: hex.EncodeToString(m.ChunkHashes[i][:]), Offset: int64(i) * m.ChunkSize, Size: m.ChunkLen(i)}
			}
			if err := w.WriteJSON(protocol.MsgManifestRes, res); err != nil {
				return err
			}
		case protocol.MsgChunkReq:
			var req protocol.ChunkRequestPayload
			if json.Unmarshal(f.Payload, &req) != nil {
				return fmt.Errorf("invalid v1 chunk request")
			}
			sh := s.lookup(req.FileHash)
			if sh == nil {
				w.WriteJSON(protocol.MsgError, protocol.ErrorPayload{Code: 404, Message: "file not found"})
				continue
			}
			for _, idx := range req.ChunkIndices {
				if int(idx) >= sh.m.NumChunks() {
					w.WriteJSON(protocol.MsgError, protocol.ErrorPayload{Code: 400, Message: "chunk out of range"})
					continue
				}
				n := int(sh.m.ChunkLen(int(idx)))
				buf := make([]byte, 9+n)
				binary.BigEndian.PutUint32(buf[0:4], uint32(4+n))
				buf[4] = protocol.MsgChunkRes
				binary.BigEndian.PutUint32(buf[5:9], idx)
				if _, err := sh.f.ReadAt(buf[9:], int64(idx)*sh.m.ChunkSize); err != nil && err != io.EOF {
					w.WriteJSON(protocol.MsgError, protocol.ErrorPayload{Code: 500, Message: "read failed"})
					continue
				}
				if _, err := rw.Write(buf); err != nil {
					return err
				}
				s.ServedChunks.Add(1)
				s.ServedBytes.Add(uint64(n))
			}
		default:
			w.WriteJSON(protocol.MsgError, protocol.ErrorPayload{Code: 400, Message: "unsupported message"})
		}
	}
}
