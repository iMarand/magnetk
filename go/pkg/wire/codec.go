// Package wire implements the Magnetk v2 binary protocol.
//
// Every message is a frame:
//
//	+----------------+--------+------------------+
//	| length: u32 BE | type:u8| body (length B)  |
//	+----------------+--------+------------------+
//
// The 5-byte header is identical to Magnetk v1, so v1 and v2 frames can be
// read by the same code. Bodies are a sequence of big-endian integers,
// length-prefixed strings (u16) and, as the last field, a raw payload that
// runs to the end of the frame (no extra length prefix, no JSON, no base64).
package wire

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// HeaderSize is the size of the frame header.
const HeaderSize = 5

// DefaultMaxFrame is the default upper bound for one frame body (16 MiB).
const DefaultMaxFrame = 16 << 20

var (
	// ErrFrameTooLarge is returned when a frame exceeds the configured limit.
	ErrFrameTooLarge = errors.New("wire: frame too large")
	// ErrMalformed is returned when a body cannot be decoded.
	ErrMalformed = errors.New("wire: malformed frame")
)

// ===== Encoder =====

// Enc builds one frame. The zero value is not usable; call NewEnc.
type Enc struct {
	b []byte
}

// NewEnc starts a frame of the given type; sizeHint is the expected body size.
func NewEnc(typ uint8, sizeHint int) *Enc {
	b := make([]byte, HeaderSize, HeaderSize+sizeHint)
	b[4] = typ
	return &Enc{b: b}
}

func (e *Enc) U8(v uint8) *Enc   { e.b = append(e.b, v); return e }
func (e *Enc) U16(v uint16) *Enc { e.b = binary.BigEndian.AppendUint16(e.b, v); return e }
func (e *Enc) U32(v uint32) *Enc { e.b = binary.BigEndian.AppendUint32(e.b, v); return e }
func (e *Enc) U64(v uint64) *Enc { e.b = binary.BigEndian.AppendUint64(e.b, v); return e }

// Str appends a u16-length-prefixed string (truncated at 65535 bytes).
func (e *Enc) Str(s string) *Enc {
	if len(s) > 0xFFFF {
		s = s[:0xFFFF]
	}
	e.b = binary.BigEndian.AppendUint16(e.b, uint16(len(s)))
	e.b = append(e.b, s...)
	return e
}

// Strs appends a u16 count followed by strings.
func (e *Enc) Strs(ss []string) *Enc {
	n := len(ss)
	if n > 0xFFFF {
		n = 0xFFFF
	}
	e.U16(uint16(n))
	for _, s := range ss[:n] {
		e.Str(s)
	}
	return e
}

// Bytes appends a u32-length-prefixed byte slice.
func (e *Enc) Bytes(p []byte) *Enc {
	e.b = binary.BigEndian.AppendUint32(e.b, uint32(len(p)))
	e.b = append(e.b, p...)
	return e
}

// Raw appends bytes without a length prefix (only valid as the last field).
func (e *Enc) Raw(p []byte) *Enc {
	e.b = append(e.b, p...)
	return e
}

// Frame finalises the header and returns the complete frame.
func (e *Enc) Frame() []byte {
	binary.BigEndian.PutUint32(e.b[:4], uint32(len(e.b)-HeaderSize))
	return e.b
}

// ===== Decoder =====

// Dec reads fields from a frame body. Errors are sticky: check Err() once.
type Dec struct {
	b   []byte
	err error
}

// NewDec wraps a frame body.
func NewDec(body []byte) *Dec { return &Dec{b: body} }

func (d *Dec) need(n int) bool {
	if d.err != nil {
		return false
	}
	if len(d.b) < n {
		d.err = ErrMalformed
		return false
	}
	return true
}

func (d *Dec) U8() uint8 {
	if !d.need(1) {
		return 0
	}
	v := d.b[0]
	d.b = d.b[1:]
	return v
}

func (d *Dec) U16() uint16 {
	if !d.need(2) {
		return 0
	}
	v := binary.BigEndian.Uint16(d.b)
	d.b = d.b[2:]
	return v
}

func (d *Dec) U32() uint32 {
	if !d.need(4) {
		return 0
	}
	v := binary.BigEndian.Uint32(d.b)
	d.b = d.b[4:]
	return v
}

func (d *Dec) U64() uint64 {
	if !d.need(8) {
		return 0
	}
	v := binary.BigEndian.Uint64(d.b)
	d.b = d.b[8:]
	return v
}

func (d *Dec) Str() string {
	n := int(d.U16())
	if !d.need(n) {
		return ""
	}
	s := string(d.b[:n])
	d.b = d.b[n:]
	return s
}

func (d *Dec) Strs() []string {
	n := int(d.U16())
	if d.err != nil || n == 0 {
		return nil
	}
	out := make([]string, 0, n)
	for i := 0; i < n && d.err == nil; i++ {
		out = append(out, d.Str())
	}
	return out
}

func (d *Dec) Bytes() []byte {
	n := int(d.U32())
	if !d.need(n) {
		return nil
	}
	p := d.b[:n]
	d.b = d.b[n:]
	return p
}

// Rest returns the remaining body (aliases the frame buffer).
func (d *Dec) Rest() []byte {
	if d.err != nil {
		return nil
	}
	p := d.b
	d.b = nil
	return p
}

// Remaining returns the number of undecoded bytes.
func (d *Dec) Remaining() int { return len(d.b) }

// Err returns the first decoding error.
func (d *Dec) Err() error { return d.err }

// ===== Frame I/O =====

// ReadFrame reads one frame. The returned body is freshly allocated.
func ReadFrame(r *bufio.Reader, maxFrame int) (typ uint8, body []byte, err error) {
	var hdr [HeaderSize]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:4])
	if maxFrame > 0 && n > uint32(maxFrame) {
		return 0, nil, fmt.Errorf("%w: %d bytes (limit %d)", ErrFrameTooLarge, n, maxFrame)
	}
	body = make([]byte, n)
	if n > 0 {
		if _, err = io.ReadFull(r, body); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return 0, nil, err
		}
	}
	return hdr[4], body, nil
}

// ReadFrameInto reads one frame reusing buf when it is large enough.
func ReadFrameInto(r *bufio.Reader, maxFrame int, buf []byte) (typ uint8, body []byte, err error) {
	var hdr [HeaderSize]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:4])
	if maxFrame > 0 && n > uint32(maxFrame) {
		return 0, nil, fmt.Errorf("%w: %d bytes (limit %d)", ErrFrameTooLarge, n, maxFrame)
	}
	if cap(buf) >= int(n) {
		body = buf[:n]
	} else {
		body = make([]byte, n)
	}
	if n > 0 {
		if _, err = io.ReadFull(r, body); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return 0, nil, err
		}
	}
	return hdr[4], body, nil
}

// ReadFrameExact reads one frame without reading past its end. Use it on
// streams that switch to raw bytes after a handshake frame (pipes), where a
// buffered reader would swallow the following data.
func ReadFrameExact(r io.Reader, maxFrame int) (typ uint8, body []byte, err error) {
	var hdr [HeaderSize]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:4])
	if maxFrame > 0 && n > uint32(maxFrame) {
		return 0, nil, fmt.Errorf("%w: %d bytes (limit %d)", ErrFrameTooLarge, n, maxFrame)
	}
	body = make([]byte, n)
	if n > 0 {
		if _, err = io.ReadFull(r, body); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return 0, nil, err
		}
	}
	return hdr[4], body, nil
}

// WriteFrame writes a complete frame (as returned by Enc.Frame).
func WriteFrame(w io.Writer, frame []byte) error {
	_, err := w.Write(frame)
	return err
}
