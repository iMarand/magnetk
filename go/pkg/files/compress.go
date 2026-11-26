package files

// On-the-fly chunk compression.
//
// When a file is shared, a sample is compressed to see whether the content
// is compressible (text, logs, CSV, JSON, uncompressed images or audio).
// Already compressed formats (zip, mp4, jpg, opus) are detected that way and
// sent as they are. For compressible files, each requested chunk is
// deflated (fastest level) and sent as TChunkDataZ when it saves at least
// 10% - only to downloaders that announced ChunkAcceptCompressed, so older
// peers keep working. Chunk hashes are over the original bytes, so
// verification does not change.

import (
	"bytes"
	"compress/flate"
	"errors"
	"io"
	"os"
	"sync"
)

const (
	compressSample   = 256 << 10 // bytes sampled at share time
	compressMaxRatio = 0.85      // sample must shrink below this to enable compression
	chunkMaxRatio    = 0.90      // a chunk is sent compressed only below this
)

var flateWriters = sync.Pool{New: func() any {
	w, _ := flate.NewWriter(io.Discard, flate.BestSpeed)
	return w
}}

var flateReaders sync.Pool

// isCompressible samples the start of f.
func isCompressible(f *os.File, size int64) bool {
	n := int64(compressSample)
	if size < n {
		n = size
	}
	if n < 4<<10 {
		return false // too small to matter
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, 0); err != nil && err != io.EOF {
		return false
	}
	var out bytes.Buffer
	if deflateTo(&out, buf) != nil {
		return false
	}
	return float64(out.Len()) < compressMaxRatio*float64(n)
}

func deflateTo(dst *bytes.Buffer, raw []byte) error {
	w := flateWriters.Get().(*flate.Writer)
	defer flateWriters.Put(w)
	w.Reset(dst)
	if _, err := w.Write(raw); err != nil {
		return err
	}
	return w.Close()
}

// inflateInto decompresses comp into dst[:rawLen].
func inflateInto(dst, comp []byte, rawLen int) ([]byte, error) {
	if cap(dst) < rawLen {
		dst = make([]byte, rawLen)
	}
	dst = dst[:rawLen]
	src := bytes.NewReader(comp)
	var r io.ReadCloser
	if v := flateReaders.Get(); v != nil {
		r = v.(io.ReadCloser)
		r.(flate.Resetter).Reset(src, nil)
	} else {
		r = flate.NewReader(src)
	}
	defer flateReaders.Put(r)
	if _, err := io.ReadFull(r, dst); err != nil {
		return nil, err
	}
	// the stream must end exactly here
	var one [1]byte
	if n, _ := r.Read(one[:]); n != 0 {
		return nil, errors.New("files: compressed chunk longer than announced")
	}
	return dst, nil
}
