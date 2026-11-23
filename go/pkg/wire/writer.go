package wire

import (
	"bufio"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
)

// ErrQueueFull is returned by TrySend when the outbound queue is full.
var ErrQueueFull = errors.New("wire: outbound queue full")

// ErrWriterClosed is returned after the writer stopped.
var ErrWriterClosed = errors.New("wire: writer closed")

// QueueWriter serialises frames onto a stream from a single goroutine.
//
// Producers append complete frames to a queue bounded only by bytes; the
// writer goroutine swaps the whole queue out at once and writes it through a
// 256 KiB buffer, flushing when the queue runs empty. Under load many
// messages share one transport write; when idle a lone message goes out
// immediately. Send blocks while the queue is full (backpressure), TrySend
// fails instead (drop policy).
type QueueWriter struct {
	w        io.Writer
	maxBytes int64

	mu     sync.Mutex
	q      [][]byte
	queued int64
	closed bool

	wake    chan struct{}
	space   chan struct{}
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
	started atomic.Bool
	err     atomic.Pointer[error]

	// Stats
	Frames  atomic.Uint64
	Bytes   atomic.Uint64
	Flushes atomic.Uint64
}

// NewQueueWriter starts a writer goroutine. maxBytes bounds queued data.
func NewQueueWriter(w io.Writer, maxBytes int64) *QueueWriter {
	q := NewQueueWriterPaused(w, maxBytes)
	q.Start()
	return q
}

// NewQueueWriterPaused creates a writer that queues frames but does not
// write them until Start is called (used to order a handshake reply before
// anything else while the session is being registered).
func NewQueueWriterPaused(w io.Writer, maxBytes int64) *QueueWriter {
	if maxBytes <= 0 {
		maxBytes = 8 << 20
	}
	return &QueueWriter{
		w:        w,
		maxBytes: maxBytes,
		wake:     make(chan struct{}, 1),
		space:    make(chan struct{}, 1),
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

// Start begins writing queued frames. Calling it twice is a no-op.
func (q *QueueWriter) Start() {
	if q.started.CompareAndSwap(false, true) {
		go q.run()
	}
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (q *QueueWriter) take(batch [][]byte) [][]byte {
	q.mu.Lock()
	batch, q.q = q.q, batch[:0]
	q.queued = 0
	q.mu.Unlock()
	return batch
}

func (q *QueueWriter) run() {
	defer close(q.stopped)
	bw := bufio.NewWriterSize(q.w, 256<<10)
	var batch [][]byte
	for {
		batch = q.take(batch)
		if len(batch) == 0 {
			select {
			case <-q.wake:
				continue
			case <-q.done:
				// final drain of frames queued before Close
				batch = q.take(batch)
				for _, f := range batch {
					if _, err := bw.Write(f); err != nil {
						return
					}
				}
				bw.Flush()
				return
			}
		}
		notify(q.space)
		for i, f := range batch {
			if _, err := bw.Write(f); err != nil {
				q.fail(err)
				return
			}
			q.Frames.Add(1)
			q.Bytes.Add(uint64(len(f)))
			batch[i] = nil // let the GC reclaim the frame
		}
		q.mu.Lock()
		more := len(q.q) > 0
		q.mu.Unlock()
		if !more {
			if err := bw.Flush(); err != nil {
				q.fail(err)
				return
			}
			q.Flushes.Add(1)
		}
	}
}

func (q *QueueWriter) fail(err error) {
	q.err.CompareAndSwap(nil, &err)
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.once.Do(func() { close(q.done) })
}

// Err returns the write error that stopped the writer, if any.
func (q *QueueWriter) Err() error {
	if p := q.err.Load(); p != nil {
		return *p
	}
	return nil
}

// Queued returns the number of bytes waiting for the writer goroutine.
func (q *QueueWriter) Queued() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.queued
}

// TrySend enqueues a frame without blocking.
func (q *QueueWriter) TrySend(frame []byte) error {
	n := int64(len(frame))
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrWriterClosed
	}
	if q.queued > 0 && q.queued+n > q.maxBytes {
		q.mu.Unlock()
		return ErrQueueFull
	}
	q.q = append(q.q, frame)
	q.queued += n
	q.mu.Unlock()
	notify(q.wake)
	return nil
}

// Send enqueues a frame, waiting for queue space until ctx ends.
func (q *QueueWriter) Send(ctx context.Context, frame []byte) error {
	for {
		err := q.TrySend(frame)
		if err != ErrQueueFull {
			return err
		}
		select {
		case <-q.space:
		case <-q.done:
			return ErrWriterClosed
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Close stops the writer after flushing frames that are already queued.
func (q *QueueWriter) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.once.Do(func() { close(q.done) })
	if q.started.CompareAndSwap(false, true) {
		go q.run() // flush what was queued before Start
	}
	<-q.stopped
}

// Done is closed when the writer stops.
func (q *QueueWriter) Done() <-chan struct{} { return q.done }
