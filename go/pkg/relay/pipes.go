package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/wire"
)

// pipeError carries a PIPE_REJECT code.
type pipeError struct {
	code   uint16
	reason string
}

func (e *pipeError) Error() string { return fmt.Sprintf("pipe rejected (%d): %s", e.code, e.reason) }

// acceptPipes serves streams a node (or relay link) opens after the control
// stream: each one starts with PIPE_OPEN.
func (s *Server) acceptPipes(ss *session) {
	for {
		st, err := ss.conn.AcceptStream(ss.ctx)
		if err != nil {
			return
		}
		go s.handlePipeStream(ss, st)
	}
}

func (s *Server) handlePipeStream(ss *session, st netx.Stream) {
	st.SetReadDeadline(time.Now().Add(15 * time.Second))
	typ, body, err := wire.ReadFrameExact(st, 64<<10)
	st.SetReadDeadline(time.Time{})
	if err != nil || typ != wire.TPipeOpen {
		st.Close()
		return
	}
	po, err := wire.DecodePipeOpen(body)
	if err != nil {
		st.Close()
		return
	}
	from := ss.id
	fromLink := ss.link != nil
	if fromLink {
		from = po.From
	}

	if int(s.pipes.Load()) >= s.cfg.MaxPipes {
		writeReject(st, wire.ErrCodeOverloaded, "relay pipe limit reached")
		return
	}

	ctx, cancel := context.WithTimeout(ss.ctx, 20*time.Second)
	target, err := s.openPipeTo(ctx, from, po.To, po.Service, po.Meta, !fromLink)
	cancel()
	if err != nil {
		var pe *pipeError
		if errors.As(err, &pe) {
			writeReject(st, pe.code, pe.reason)
		} else {
			writeReject(st, wire.ErrCodeNoRoute, err.Error())
		}
		return
	}
	if _, err := st.Write(wire.EncodePipeAccept()); err != nil {
		st.Close()
		target.Close()
		return
	}
	s.splice(st, target)
}

func writeReject(st netx.Stream, code uint16, reason string) {
	r := wire.PipeReject{Code: code, Reason: reason}
	st.SetWriteDeadline(time.Now().Add(2 * time.Second))
	st.Write(r.Encode())
	st.CloseWrite()
	st.Close()
}

// openPipeTo opens a stream to node `to` (local, or through a relay link when
// allowForward) and completes the pipe handshake. The returned stream is
// ready for raw data.
func (s *Server) openPipeTo(ctx context.Context, from, to, service, meta string, allowForward bool) (netx.Stream, error) {
	s.mu.RLock()
	target := s.sessions[to]
	var remote *remoteNode
	if target == nil && allowForward {
		remote = s.routes[to]
	}
	s.mu.RUnlock()

	var (
		st  netx.Stream
		err error
	)
	switch {
	case target != nil:
		st, err = target.conn.OpenStream(ctx)
		if err != nil {
			return nil, err
		}
		pi := wire.PipeIncoming{From: from, Service: service, Meta: meta, Via: wire.ViaRelay}
		_, err = st.Write(pi.Encode())
	case remote != nil:
		st, err = remote.link.sess.conn.OpenStream(ctx)
		if err != nil {
			return nil, err
		}
		po := wire.PipeOpen{From: from, To: to, Service: service, Meta: meta}
		_, err = st.Write(po.Encode())
	default:
		return nil, &pipeError{wire.ErrCodeNoRoute, "node " + to + " is not connected"}
	}
	if err != nil {
		st.Close()
		return nil, err
	}

	if dl, ok := ctx.Deadline(); ok {
		st.SetReadDeadline(dl)
	}
	typ, body, err := wire.ReadFrameExact(st, 64<<10)
	st.SetReadDeadline(time.Time{})
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("pipe handshake with %s: %w", to, err)
	}
	switch typ {
	case wire.TPipeAccept:
		return st, nil
	case wire.TPipeReject:
		rj, _ := wire.DecodePipeReject(body)
		st.Close()
		return nil, &pipeError{rj.Code, rj.Reason}
	default:
		st.Close()
		return nil, fmt.Errorf("pipe handshake with %s: unexpected frame 0x%02x", to, typ)
	}
}

// splice copies both directions until both ends finished, propagating
// half-close (EOF) from one side to the other.
func (s *Server) splice(a, b netx.Stream) {
	s.pipes.Add(1)
	s.st.pipesTotal.Add(1)
	defer s.pipes.Add(-1)

	var wg sync.WaitGroup
	wg.Add(2)
	var failOnce sync.Once
	fail := func() {
		failOnce.Do(func() {
			a.Close()
			b.Close()
		})
	}
	cp := func(dst, src netx.Stream) {
		defer wg.Done()
		bp := s.bufPool.Get().(*[]byte)
		defer s.bufPool.Put(bp)
		n, err := copyBuf(dst, src, *bp)
		s.st.pipeBytes.Add(uint64(n))
		if err != nil {
			fail()
			return
		}
		dst.CloseWrite()
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	a.Close()
	b.Close()
}

// copyBuf is io.CopyBuffer without the ReaderFrom/WriterTo detours, so the
// pooled buffer is always used.
func copyBuf(dst io.Writer, src io.Reader, buf []byte) (int64, error) {
	var total int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			w, werr := dst.Write(buf[:n])
			total += int64(w)
			if werr != nil {
				return total, werr
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return total, nil
			}
			return total, rerr
		}
	}
}
