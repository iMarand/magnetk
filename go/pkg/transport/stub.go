//go:build !cgo

package transport

import (
	"errors"
	"fmt"
)

var (
	ErrNoCGO = errors.New("transport: CGO disabled, MsQuic unavailable")
)

type Config struct {
	BindAddress       string
	Port              uint16
	CertFile          string
	KeyFile           string
	NoCertValidation  bool
	ALPN              string
	MaxConnections    uint32
	IdleTimeoutMs     uint32
	SendBufferSize    uint32
	RecvBufferSize    uint32
	MaxStreamsPerConn uint16
	MaxThroughput     bool
}

func DefaultConfig() Config {
	return Config{}
}

type Transport struct{}

func NewTransport(cfg Config) (*Transport, error) {
	fmt.Println("[transport] Warning: CGO disabled. Using stub transport (no data transfer).")
	return &Transport{}, nil
}

func (t *Transport) Close() {}

func (t *Transport) Connect(host string, port uint16) (*Connection, error) {
	return nil, ErrNoCGO
}

func (t *Transport) Listen(onStream StreamHandler) (*Listener, error) {
	return &Listener{}, nil
}

type Listener struct{}

func (l *Listener) Close() {}

type Connection struct{}

func (c *Connection) Close() {}

func (c *Connection) OpenStream() (*Stream, error) {
	return nil, ErrNoCGO
}

type Stream struct{}

func (s *Stream) Read(p []byte) (n int, err error) {
	return 0, ErrNoCGO
}

func (s *Stream) Write(p []byte) (n int, err error) {
	return 0, ErrNoCGO
}

func (s *Stream) Close() {}

func (s *Stream) ReadFull(buf []byte) error {
	return ErrNoCGO
}

type StreamHandler func(stream *Stream, conn *Connection)
