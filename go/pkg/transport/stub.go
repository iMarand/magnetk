//go:build !cgo || nomsquic

package transport

import (
	"context"
	"time"
)

// Available reports whether MsQuic is compiled in.
func Available() bool { return false }

// Transport is unavailable in this build; New always fails.
type Transport struct{}

// New always returns ErrUnavailable in builds without MsQuic.
func New(Config) (*Transport, error) { return nil, ErrUnavailable }

func (t *Transport) Config() Config                                      { return Config{} }
func (t *Transport) Close() error                                        { return nil }
func (t *Transport) Listen() (*Listener, error)                          { return nil, ErrUnavailable }
func (t *Transport) ListenOn(string, uint16) (*Listener, error)          { return nil, ErrUnavailable }
func (t *Transport) Dial(context.Context, string, uint16) (*Conn, error) { return nil, ErrUnavailable }

type Listener struct{}

func (l *Listener) Port() uint16                          { return 0 }
func (l *Listener) Accept(context.Context) (*Conn, error) { return nil, ErrUnavailable }
func (l *Listener) Close() error                          { return nil }

type Conn struct{}

func (c *Conn) RemoteAddr() string                              { return "" }
func (c *Conn) Done() <-chan struct{}                           { ch := make(chan struct{}); close(ch); return ch }
func (c *Conn) Alive() bool                                     { return false }
func (c *Conn) OpenStream(context.Context) (*Stream, error)     { return nil, ErrUnavailable }
func (c *Conn) AcceptStream(context.Context) (*Stream, error)   { return nil, ErrUnavailable }
func (c *Conn) MaxDatagram() int                                { return 0 }
func (c *Conn) SendDatagram([]byte) error                       { return ErrUnavailable }
func (c *Conn) ReceiveDatagram(context.Context) ([]byte, error) { return nil, ErrUnavailable }
func (c *Conn) Stats() ConnStats                                { return ConnStats{} }
func (c *Conn) CloseWithError(uint64) error                     { return nil }
func (c *Conn) Close() error                                    { return nil }

type Stream struct{}

func (s *Stream) ID() uint64                       { return 0 }
func (s *Stream) Conn() *Conn                      { return nil }
func (s *Stream) Read([]byte) (int, error)         { return 0, ErrUnavailable }
func (s *Stream) Write([]byte) (int, error)        { return 0, ErrUnavailable }
func (s *Stream) Flush(context.Context) error      { return ErrUnavailable }
func (s *Stream) CloseWrite() error                { return ErrUnavailable }
func (s *Stream) Abort(uint64)                     {}
func (s *Stream) Close() error                     { return nil }
func (s *Stream) SetDeadline(time.Time) error      { return nil }
func (s *Stream) SetReadDeadline(time.Time) error  { return nil }
func (s *Stream) SetWriteDeadline(time.Time) error { return nil }
