package files

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iMarand/magnetk/go/pkg/wire"
)

// Source is a provider of a file: Open returns a fresh stream speaking the
// v2 file protocol (MANIFEST_REQ / CHUNK_REQ).
type Source interface {
	ID() string
	Open(ctx context.Context) (io.ReadWriteCloser, error)
}

// Progress is reported while downloading.
type Progress struct {
	Name        string
	Size        int64
	Done        int64
	Chunks      int
	ChunksDone  int
	BytesPerSec float64
	ETA         time.Duration
	Elapsed     time.Duration
	Sources     int
	Streams     int
	Window      int   // chunk requests in flight on all streams (adaptive)
	WireBytes   int64 // bytes received on the wire (less than Done-resumed when compressed)
}

// Percent returns completion in percent.
func (p Progress) Percent() float64 {
	if p.Size == 0 {
		return 100
	}
	return float64(p.Done) * 100 / float64(p.Size)
}

// Options configures Download.
type Options struct {
	Hash string // required: expected file hash
	Root string // expected manifest root (from the link); "" = verify whole file at the end
	Out  string // output path (file) — written to Out+".mkpart" and renamed at the end

	StreamsPerSource int // parallel streams per provider (default 4)
	// Pipeline is the number of chunk requests kept in flight per stream.
	// 0 = adaptive: starts at 4 and grows (up to MaxPipeline) while the
	// measured throughput improves, shrinks when it drops - more data in
	// flight on fast links, less on slow or unstable ones.
	Pipeline    int
	MaxPipeline int // adaptive upper bound (default 32)
	MaxSources  int // default 8
	Resume      bool

	// Offset and Length select a byte range (0, 0 = the whole file). Only
	// the chunks covering the range are fetched; each is verified, so a
	// range needs the manifest root (Root, i.e. a link). Out then receives
	// exactly Length bytes.
	Offset int64
	Length int64

	// NoCompression does not ask providers for compressed chunks.
	NoCompression bool

	Progress         func(Progress)
	ProgressInterval time.Duration // default 250ms

	Logger *log.Logger
}

// Result describes a finished download.
type Result struct {
	Path      string
	Manifest  *Manifest
	Bytes     int64 // bytes transferred in this run
	Resumed   int64 // bytes already present from a previous run
	Elapsed   time.Duration
	PerSource map[string]int64
	WireBytes int64 // bytes received on the wire (compression saves the difference)
	Offset    int64 // range start (0 for whole files)
}

type resumeState struct {
	Hash      string `json:"hash"`
	Size      int64  `json:"size"`
	ChunkSize int64  `json:"chunk_size"`
	Done      string `json:"done"` // base64 bitmap
	Offset    int64  `json:"offset,omitempty"`
	End       int64  `json:"end,omitempty"`
}

// Download fetches a file from one or more sources in parallel.
func Download(ctx context.Context, sources []Source, opts Options) (*Result, error) {
	if len(sources) == 0 {
		return nil, errors.New("files: no sources")
	}
	if opts.StreamsPerSource <= 0 {
		opts.StreamsPerSource = 4
	}
	adaptive := opts.Pipeline <= 0
	if adaptive {
		opts.Pipeline = 4
	}
	if opts.MaxPipeline <= 0 {
		opts.MaxPipeline = 32
	}
	if opts.MaxSources <= 0 {
		opts.MaxSources = 8
	}
	if opts.ProgressInterval <= 0 {
		opts.ProgressInterval = 250 * time.Millisecond
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	if len(sources) > opts.MaxSources {
		sources = sources[:opts.MaxSources]
	}

	// 1. Manifest from the first source that answers correctly.
	var (
		m         *Manifest
		firstConn io.ReadWriteCloser
		firstSrc  int
		lastErr   error
	)
	for i, src := range sources {
		conn, man, err := fetchManifest(ctx, src, opts.Hash, opts.Root)
		if err != nil {
			lastErr = err
			logger.Printf("[files] manifest from %s failed: %v", src.ID(), err)
			continue
		}
		m, firstConn, firstSrc = man, conn, i
		break
	}
	if m == nil {
		return nil, fmt.Errorf("files: could not get a valid manifest: %w", lastErr)
	}
	sources = append([]Source{sources[firstSrc]}, append(append([]Source{}, sources[:firstSrc]...), sources[firstSrc+1:]...)...)

	// Byte range.
	start, end := int64(0), m.Size
	ranged := opts.Offset != 0 || opts.Length != 0
	if ranged {
		if opts.Root == "" {
			firstConn.Close()
			return nil, errors.New("files: a byte range needs the manifest root (download from a link)")
		}
		if opts.Offset < 0 || opts.Length < 0 || opts.Offset >= m.Size {
			firstConn.Close()
			return nil, fmt.Errorf("files: range %d+%d outside the file (%d bytes)", opts.Offset, opts.Length, m.Size)
		}
		start = opts.Offset
		if opts.Length > 0 && start+opts.Length < end {
			end = start + opts.Length
		}
	}
	firstChunk, lastChunk := int(start/m.ChunkSize), int((end-1)/m.ChunkSize)
	if m.Size == 0 {
		firstChunk, lastChunk = 0, -1
	}
	outSize := end - start
	// overlap returns the part of chunk idx inside the range.
	overlap := func(idx int, clen int64) (lo, hi int64) {
		cs := int64(idx) * m.ChunkSize
		lo, hi = max(start, cs), min(end, cs+clen)
		return lo - cs, hi - cs
	}

	// 2. Output file + resume state.
	if opts.Out == "" {
		opts.Out = m.Name
	}
	if err := os.MkdirAll(filepath.Dir(opts.Out), 0o755); err != nil && filepath.Dir(opts.Out) != "." {
		firstConn.Close()
		return nil, err
	}
	part := opts.Out + ".mkpart"
	statePath := part + ".state"
	n := m.NumChunks()
	done := make([]bool, n)
	var resumed int64
	if opts.Resume {
		if st, err := loadState(statePath); err == nil && st.Hash == m.Hash && st.ChunkSize == m.ChunkSize && st.Size == m.Size &&
			st.Offset == start && (st.End == end || (st.End == 0 && !ranged)) {
			if bm, err := base64.StdEncoding.DecodeString(st.Done); err == nil {
				for i := firstChunk; i <= lastChunk && i/8 < len(bm); i++ {
					if bm[i/8]&(1<<(i%8)) != 0 {
						done[i] = true
						lo, hi := overlap(i, m.ChunkLen(i))
						resumed += hi - lo
					}
				}
			}
		}
	}
	flags := os.O_CREATE | os.O_RDWR
	if resumed == 0 {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		firstConn.Close()
		return nil, err
	}
	if err := f.Truncate(outSize); err != nil {
		f.Close()
		firstConn.Close()
		return nil, err
	}

	// 3. Work queue.
	queue := make(chan int, n+1)
	remaining := int64(0)
	for i := firstChunk; i <= lastChunk; i++ {
		if !done[i] {
			queue <- i
			remaining++
		}
	}
	var (
		left       atomic.Int64
		bytesDone  atomic.Int64
		chunksDone atomic.Int64
		wireBytes  atomic.Int64
		window     atomic.Int32
		streams    atomic.Int32
		stateMu    sync.Mutex
		perSrcMu   sync.Mutex
		perSource  = make(map[string]int64)
		allDone    = make(chan struct{})
		doneOnce   sync.Once
		writeErr   atomic.Pointer[error]
	)
	left.Store(remaining)
	bytesDone.Store(resumed)
	chunksDone.Store(int64(lastChunk-firstChunk+1) - remaining)
	if remaining == 0 {
		doneOnce.Do(func() { close(allDone) })
	}

	saveState := func() {
		stateMu.Lock()
		bm := make([]byte, (n+7)/8)
		for i, d := range done {
			if d {
				bm[i/8] |= 1 << (i % 8)
			}
		}
		stateMu.Unlock()
		st := resumeState{Hash: m.Hash, Size: m.Size, ChunkSize: m.ChunkSize, Done: base64.StdEncoding.EncodeToString(bm)}
		if ranged {
			st.Offset, st.End = start, end
		}
		if data, err := json.Marshal(st); err == nil {
			os.WriteFile(statePath, data, 0o644)
		}
	}

	complete := func(idx int, data []byte, src string) {
		lo, hi := overlap(idx, int64(len(data)))
		data = data[lo:hi]
		if _, err := f.WriteAt(data, int64(idx)*m.ChunkSize+lo-start); err != nil {
			e := fmt.Errorf("write %s: %w", part, err)
			writeErr.CompareAndSwap(nil, &e)
			doneOnce.Do(func() { close(allDone) })
			return
		}
		stateMu.Lock()
		done[idx] = true
		stateMu.Unlock()
		bytesDone.Add(int64(len(data)))
		chunksDone.Add(1)
		perSrcMu.Lock()
		perSource[src] += int64(len(data))
		perSrcMu.Unlock()
		if left.Add(-1) == 0 {
			doneOnce.Do(func() { close(allDone) })
		}
	}

	dlCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	t0 := time.Now()

	// 4. Progress reporter + periodic state save.
	var repWG sync.WaitGroup
	repWG.Add(1)
	go func() {
		defer repWG.Done()
		t := time.NewTicker(opts.ProgressInterval)
		defer t.Stop()
		lastSave := time.Now()
		report := func() {
			if opts.Progress == nil {
				return
			}
			el := time.Since(t0)
			d := bytesDone.Load()
			p := Progress{Name: m.Name, Size: outSize, Done: d, Chunks: lastChunk - firstChunk + 1, ChunksDone: int(chunksDone.Load()),
				Elapsed: el, Sources: len(sources), Streams: int(streams.Load()), Window: int(window.Load()), WireBytes: wireBytes.Load()}
			if sec := el.Seconds(); sec > 0 {
				p.BytesPerSec = float64(d-resumed) / sec
			}
			if p.BytesPerSec > 0 {
				p.ETA = time.Duration(float64(outSize-d) / p.BytesPerSec * float64(time.Second))
			}
			opts.Progress(p)
		}
		for {
			select {
			case <-t.C:
				report()
				if opts.Resume && time.Since(lastSave) > time.Second {
					saveState()
					lastSave = time.Now()
				}
			case <-allDone:
				report()
				return
			case <-dlCtx.Done():
				return
			}
		}
	}()

	// 5. Workers.
	var wg sync.WaitGroup
	for si, src := range sources {
		for k := 0; k < opts.StreamsPerSource; k++ {
			var conn io.ReadWriteCloser
			if si == 0 && k == 0 {
				conn = firstConn
			}
			wg.Add(1)
			go func(src Source, conn io.ReadWriteCloser) {
				defer wg.Done()
				ws := workerSettings{pipeline: opts.Pipeline, adaptive: adaptive, maxPipeline: opts.MaxPipeline,
					acceptZ: !opts.NoCompression, wireBytes: &wireBytes, window: &window}
				runWorker(dlCtx, src, conn, m, ws, queue, allDone, &streams, complete, logger)
			}(src, conn)
		}
	}
	workersDone := make(chan struct{})
	go func() { wg.Wait(); close(workersDone) }()

	var runErr error
	select {
	case <-allDone:
	case <-workersDone:
		select {
		case <-allDone:
		default:
			runErr = fmt.Errorf("files: all sources failed with %d chunks missing", left.Load())
		}
	case <-ctx.Done():
		runErr = ctx.Err()
	}
	cancel()
	<-workersDone
	repWG.Wait()
	if p := writeErr.Load(); p != nil && runErr == nil {
		runErr = *p
	}

	if runErr != nil {
		if opts.Resume {
			saveState()
		}
		f.Close()
		return nil, runErr
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()

	// 6. Integrity: chunks were verified against the manifest. Without a
	// root from the link the manifest itself is unauthenticated, so check
	// the whole file too.
	if opts.Root == "" && !ranged {
		if err := VerifyFile(part, m.Hash); err != nil {
			os.Remove(part)
			os.Remove(statePath)
			return nil, err
		}
	}
	os.Remove(opts.Out)
	if err := os.Rename(part, opts.Out); err != nil {
		return nil, err
	}
	os.Remove(statePath)
	return &Result{Path: opts.Out, Manifest: m, Bytes: bytesDone.Load() - resumed, Resumed: resumed, Elapsed: time.Since(t0),
		PerSource: perSource, WireBytes: wireBytes.Load(), Offset: start}, nil
}

func fetchManifest(ctx context.Context, src Source, hash, root string) (io.ReadWriteCloser, *Manifest, error) {
	octx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, err := src.Open(octx)
	if err != nil {
		return nil, nil, err
	}
	if dl, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		d, _ := octx.Deadline()
		dl.SetDeadline(d)
		defer dl.SetDeadline(time.Time{})
	}
	if _, err := conn.Write(wire.EncodeManifestReq(hash)); err != nil {
		conn.Close()
		return nil, nil, err
	}
	typ, body, err := wire.ReadFrameExact(conn, 64<<20)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	switch typ {
	case wire.TManifestRes:
		w, err := wire.DecodeManifestRes(body)
		if err != nil {
			conn.Close()
			return nil, nil, err
		}
		m, err := FromWire(&w, hash, root)
		if err != nil {
			conn.Close()
			return nil, nil, err
		}
		return conn, m, nil
	case wire.TFileError:
		fe, _ := wire.DecodeFileError(body)
		conn.Close()
		return nil, nil, fmt.Errorf("provider: %s", fe.Msg)
	default:
		conn.Close()
		return nil, nil, fmt.Errorf("unexpected frame 0x%02x", typ)
	}
}

type workerSettings struct {
	pipeline    int
	adaptive    bool
	maxPipeline int
	acceptZ     bool
	wireBytes   *atomic.Int64
	window      *atomic.Int32
}

// runWorker keeps chunk requests in flight on one stream.
func runWorker(ctx context.Context, src Source, conn io.ReadWriteCloser, m *Manifest, ws workerSettings,
	queue chan int, allDone <-chan struct{}, streams *atomic.Int32,
	complete func(int, []byte, string), logger *log.Logger) {

	failures := 0
	for {
		if conn == nil {
			oc, cancel := context.WithTimeout(ctx, 20*time.Second)
			c, err := src.Open(oc)
			cancel()
			if err != nil {
				failures++
				if failures >= 3 || ctx.Err() != nil {
					return
				}
				select {
				case <-time.After(time.Duration(failures) * 500 * time.Millisecond):
					continue
				case <-ctx.Done():
					return
				case <-allDone:
					return
				}
			}
			conn = c
		}
		streams.Add(1)
		err := serveStream(ctx, src.ID(), conn, m, ws, queue, allDone, complete)
		streams.Add(-1)
		conn.Close()
		conn = nil
		if err == nil || ctx.Err() != nil {
			return
		}
		select {
		case <-allDone:
			return
		default:
		}
		logger.Printf("[files] stream to %s failed: %v", src.ID(), err)
		failures++
		if failures >= 3 {
			return
		}
	}
}

// serveStream runs the request/response loop on one stream. In-flight
// chunks are put back in the queue if the stream fails.
func serveStream(ctx context.Context, srcID string, conn io.ReadWriteCloser, m *Manifest, ws workerSettings,
	queue chan int, allDone <-chan struct{}, complete func(int, []byte, string)) (err error) {

	pipeline := ws.pipeline
	ws.window.Add(int32(pipeline))
	defer func() { ws.window.Add(-int32(pipeline)) }()
	ad := adaptState{}
	var flags uint8
	if ws.acceptZ {
		flags = wire.ChunkAcceptCompressed
	}
	raw := make([]byte, 0, int(m.ChunkSize))
	var inflight []int
	defer func() {
		for _, idx := range inflight {
			queue <- idx
		}
	}()
	bw := bufio.NewWriterSize(conn, 4096)
	br := bufio.NewReaderSize(conn, 256<<10)
	buf := make([]byte, 0, wire.HeaderSize+4+int(m.ChunkSize))

	// Abort blocking reads when the download ends or is cancelled.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stop:
		}
	}()

	request := func(idx int) error {
		req := wire.ChunkReq{Hash: m.Hash, Indices: []uint32{uint32(idx)}, Flags: flags}
		_, err := bw.Write(req.Encode())
		return err
	}

	for {
		// Fill the pipeline.
		added := false
		for len(inflight) < pipeline {
			select {
			case idx := <-queue:
				if err := request(idx); err != nil {
					queue <- idx
					return err
				}
				inflight = append(inflight, idx)
				added = true
				continue
			default:
			}
			break
		}
		if added {
			if err := bw.Flush(); err != nil {
				return err
			}
		}
		if len(inflight) == 0 {
			select {
			case idx := <-queue:
				queue <- idx // let the fill loop pick it up
				continue
			case <-allDone:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		typ, body, err := wire.ReadFrameInto(br, wire.HeaderSize+4+int(m.ChunkSize)+64, buf)
		if err != nil {
			return err
		}
		switch typ {
		case wire.TChunkData, wire.TChunkDataZ:
			if len(body) < 4 {
				return wire.ErrMalformed
			}
			idx := int(uint32(body[0])<<24 | uint32(body[1])<<16 | uint32(body[2])<<8 | uint32(body[3]))
			data := body[4:]
			ws.wireBytes.Add(int64(wire.HeaderSize + len(body)))
			if typ == wire.TChunkDataZ {
				if len(body) < 8 {
					return wire.ErrMalformed
				}
				rawLen := int(uint32(body[4])<<24 | uint32(body[5])<<16 | uint32(body[6])<<8 | uint32(body[7]))
				if int64(rawLen) != m.ChunkLen(idx) {
					return fmt.Errorf("compressed chunk %d has wrong length", idx)
				}
				if raw, err = inflateInto(raw, body[8:], rawLen); err != nil {
					return fmt.Errorf("chunk %d from %s: %w", idx, srcID, err)
				}
				data = raw
			}
			if idx != inflight[0] {
				return fmt.Errorf("chunk %d arrived, expected %d", idx, inflight[0])
			}
			inflight = inflight[1:]
			if int64(len(data)) != m.ChunkLen(idx) || sha256.Sum256(data) != m.ChunkHashes[idx] {
				queue <- idx // corrupt or wrong data: fetch again (maybe elsewhere)
				return fmt.Errorf("chunk %d from %s failed verification", idx, srcID)
			}
			complete(idx, data, srcID)
			if ws.adaptive {
				if np := ad.sample(int64(len(data)), pipeline, ws.maxPipeline); np != pipeline {
					ws.window.Add(int32(np - pipeline))
					pipeline = np
				}
			}
		case wire.TFileError:
			fe, _ := wire.DecodeFileError(body)
			return fmt.Errorf("provider %s: %s", srcID, fe.Msg)
		default:
			return fmt.Errorf("unexpected frame 0x%02x", typ)
		}
	}
}

func loadState(path string) (*resumeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st resumeState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// adaptState tunes the pipeline depth of one stream (AIMD on throughput):
// every 8 chunks it compares the throughput with the previous window; while
// it improves the stream asks for more chunks at once, when it falls it
// backs off. Fast links fill up quickly; slow or lossy ones are not flooded.
type adaptState struct {
	n        int
	bytes    int64
	since    time.Time
	lastRate float64
}

func (a *adaptState) sample(bytes int64, pipeline, maxPipeline int) int {
	if a.since.IsZero() {
		a.since = time.Now()
	}
	a.n++
	a.bytes += bytes
	if a.n < 8 {
		return pipeline
	}
	el := time.Since(a.since).Seconds()
	rate := float64(a.bytes) / max(el, 1e-6)
	prev := a.lastRate
	a.n, a.bytes, a.since, a.lastRate = 0, 0, time.Now(), rate
	switch {
	case prev == 0 || rate > prev*1.05:
		return min(maxPipeline, pipeline+max(1, pipeline/2)) // still improving: open up
	case rate < prev*0.8:
		return max(2, pipeline*3/4) // got worse: back off
	}
	return pipeline
}
