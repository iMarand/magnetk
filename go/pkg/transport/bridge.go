// Package transport provides a Go wrapper over the C/MsQuic transport layer.
//
// It uses CGo to call into the native MsQuic-based transport for high-performance
// QUIC connections. The Go layer provides io.Reader/io.Writer compatible interfaces
// while the actual QUIC operations are handled by the C library.
package transport

/*
#cgo CFLAGS: -I${SRCDIR}/../../../c/include -I${SRCDIR}/../../../msquic/src/inc -DMAGNETK_STATIC
#cgo windows LDFLAGS: -L${SRCDIR}/../../../msquic/artifacts/bin/windows/x64_Debug_schannel -lmsquic -lws2_32 -lcrypt32 -lncrypt
#cgo linux LDFLAGS: -L${SRCDIR}/../../../msquic/artifacts/bin/linux/x64_Debug_openssl -lmsquic -lpthread
#cgo darwin LDFLAGS: -L${SRCDIR}/../../../msquic/artifacts/bin/macos/x64_Debug_openssl -lmsquic -lpthread

#include "transport.h"


#include <stdlib.h>
*/
import "C"
import (
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

// Status codes matching C definitions
var (
	ErrInit         = errors.New("transport: initialization failed")
	ErrConfig       = errors.New("transport: configuration error")
	ErrListen       = errors.New("transport: listen failed")
	ErrConnect      = errors.New("transport: connection failed")
	ErrSend         = errors.New("transport: send failed")
	ErrRecv         = errors.New("transport: receive failed")
	ErrTimeout      = errors.New("transport: timeout")
	ErrClosed       = errors.New("transport: connection closed")
	ErrTLS          = errors.New("transport: TLS error")
	ErrMemory       = errors.New("transport: out of memory")
	ErrInvalidParam = errors.New("transport: invalid parameter")
	ErrStream       = errors.New("transport: stream error")
	ErrNotReady     = errors.New("transport: not ready")
)

func statusToError(status C.magnetk_status_t) error {
	switch status {
	case C.MAGNETK_OK:
		return nil
	case C.MAGNETK_ERR_INIT:
		return ErrInit
	case C.MAGNETK_ERR_CONFIG:
		return ErrConfig
	case C.MAGNETK_ERR_LISTEN:
		return ErrListen
	case C.MAGNETK_ERR_CONNECT:
		return ErrConnect
	case C.MAGNETK_ERR_SEND:
		return ErrSend
	case C.MAGNETK_ERR_RECV:
		return ErrRecv
	case C.MAGNETK_ERR_TIMEOUT:
		return ErrTimeout
	case C.MAGNETK_ERR_CLOSED:
		return ErrClosed
	case C.MAGNETK_ERR_TLS:
		return ErrTLS
	case C.MAGNETK_ERR_MEMORY:
		return ErrMemory
	case C.MAGNETK_ERR_INVALID_PARAM:
		return ErrInvalidParam
	case C.MAGNETK_ERR_STREAM:
		return ErrStream
	case C.MAGNETK_ERR_NOT_READY:
		return ErrNotReady
	default:
		return fmt.Errorf("transport: unknown error %d", status)
	}
}

// Config holds transport configuration options.
type Config struct {
	BindAddress       string
	Port              uint16
	CertFile          string // TLS certificate file (PEM). Empty = self-signed.
	KeyFile           string // TLS key file (PEM). Empty = self-signed.
	NoCertValidation  bool   // Skip certificate validation (dev mode)
	ALPN              string
	MaxConnections    uint32
	IdleTimeoutMs     uint32
	SendBufferSize    uint32
	RecvBufferSize    uint32
	MaxStreamsPerConn uint16
	MaxThroughput     bool // Use max throughput execution profile
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		BindAddress:       "0.0.0.0",
		Port:              4002,
		NoCertValidation:  true,
		ALPN:              "magnetk",
		MaxConnections:    1000,
		IdleTimeoutMs:     30000,
		SendBufferSize:    4 * 1024 * 1024,
		RecvBufferSize:    4 * 1024 * 1024,
		MaxStreamsPerConn: 100,
		MaxThroughput:     true,
	}
}

// Transport wraps the C MsQuic transport layer.
type Transport struct {
	handle *C.magnetk_transport_t
	mu     sync.Mutex
	closed bool
}

// NewTransport creates and initializes a new MsQuic transport.
func NewTransport(cfg Config) (*Transport, error) {
	var cConfig C.magnetk_config_t
	C.magnetk_config_default(&cConfig)

	// Set string fields
	cBind := C.CString(cfg.BindAddress)
	defer C.free(unsafe.Pointer(cBind))
	cConfig.bind_address = cBind
	cConfig.port = C.uint16_t(cfg.Port)

	if cfg.CertFile != "" && cfg.KeyFile != "" {
		cCert := C.CString(cfg.CertFile)
		defer C.free(unsafe.Pointer(cCert))
		cConfig.cert_file = cCert
		
		cKey := C.CString(cfg.KeyFile)
		defer C.free(unsafe.Pointer(cKey))
		cConfig.key_file = cKey
	}

	if cfg.NoCertValidation {
		cConfig.no_cert_validation = 1
	} else {
		cConfig.no_cert_validation = 0
	}

	cAlpn := C.CString(cfg.ALPN)
	defer C.free(unsafe.Pointer(cAlpn))
	cConfig.alpn = cAlpn

	cConfig.max_connections = C.uint32_t(cfg.MaxConnections)
	cConfig.idle_timeout_ms = C.uint32_t(cfg.IdleTimeoutMs)
	cConfig.send_buffer_size = C.uint32_t(cfg.SendBufferSize)
	cConfig.recv_buffer_size = C.uint32_t(cfg.RecvBufferSize)
	cConfig.max_streams_per_conn = C.uint16_t(cfg.MaxStreamsPerConn)

	if cfg.MaxThroughput {
		cConfig.execution_profile = 1
	} else {
		cConfig.execution_profile = 0
	}

	var cTransport *C.magnetk_transport_t
	status := C.magnetk_transport_init(&cConfig, &cTransport)
	if err := statusToError(status); err != nil {
		return nil, err
	}

	return &Transport{handle: cTransport}, nil
}

// Close shuts down the transport and releases all resources.
func (t *Transport) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	C.magnetk_transport_shutdown(t.handle)
}

// Connection wraps a QUIC connection.
type Connection struct {
	handle    *C.magnetk_connection_t
	transport *Transport
	mu        sync.Mutex
	closed    bool
}

// Connect creates a new outgoing connection to the specified host and port.
// This is a blocking call — it waits until the connection is established or fails.
func (t *Transport) Connect(host string, port uint16) (*Connection, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, ErrClosed
	}
	t.mu.Unlock()

	cHost := C.CString(host)
	defer C.free(unsafe.Pointer(cHost))

	var cConn *C.magnetk_connection_t
	status := C.magnetk_connect(
		t.handle,
		cHost,
		C.uint16_t(port),
		nil, nil, nil, nil,
		&cConn)
	if err := statusToError(status); err != nil {
		return nil, fmt.Errorf("connect to %s:%d: %w", host, port, err)
	}

	return &Connection{
		handle:    cConn,
		transport: t,
	}, nil
}

// Close shuts down the connection.
func (c *Connection) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	C.magnetk_connection_close(c.handle)
}

// Stream wraps a QUIC stream with io.Reader/io.Writer interfaces.
type Stream struct {
	handle *C.magnetk_stream_t
	conn   *Connection
	mu     sync.Mutex
	closed bool
}

// OpenStream opens a new bidirectional stream on the connection.
func (c *Connection) OpenStream() (*Stream, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	c.mu.Unlock()

	var cStream *C.magnetk_stream_t
	status := C.magnetk_stream_open(
		c.handle,
		nil, nil, nil, nil,
		&cStream)
	if err := statusToError(status); err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}

	return &Stream{
		handle: cStream,
		conn:   c,
	}, nil
}

// Write sends data on the stream. Implements io.Writer.
func (s *Stream) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, ErrClosed
	}
	s.mu.Unlock()

	status := C.magnetk_stream_send_sync(
		s.handle,
		(*C.uint8_t)(unsafe.Pointer(&data[0])),
		C.uint32_t(len(data)),
		0, // not FIN
	)
	if err := statusToError(status); err != nil {
		return 0, err
	}
	return len(data), nil
}

// WriteFin sends data and closes the send direction.
func (s *Stream) WriteFin(data []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, ErrClosed
	}
	s.mu.Unlock()

	var dataPtr *C.uint8_t
	if len(data) > 0 {
		dataPtr = (*C.uint8_t)(unsafe.Pointer(&data[0]))
	}

	status := C.magnetk_stream_send_sync(
		s.handle,
		dataPtr,
		C.uint32_t(len(data)),
		1, // FIN
	)
	if err := statusToError(status); err != nil {
		return 0, err
	}
	return len(data), nil
}

// Read receives data from the stream. Implements io.Reader.
func (s *Stream) Read(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, ErrClosed
	}
	s.mu.Unlock()

	var outLen C.uint32_t
	var outFin C.int

	status := C.magnetk_stream_recv_sync(
		s.handle,
		(*C.uint8_t)(unsafe.Pointer(&buf[0])),
		C.uint32_t(len(buf)),
		&outLen,
		&outFin,
		30000, // 30s timeout
	)

	if status == C.MAGNETK_ERR_CLOSED {
		return 0, fmt.Errorf("stream closed (EOF)")
	}
	if err := statusToError(status); err != nil {
		return 0, err
	}

	return int(outLen), nil
}

// ReadFull reads exactly len(buf) bytes from the stream.
func (s *Stream) ReadFull(buf []byte) error {
	total := 0
	for total < len(buf) {
		n, err := s.Read(buf[total:])
		if err != nil {
			return err
		}
		total += n
	}
	return nil
}

// ShutdownSend gracefully closes the send direction.
func (s *Stream) ShutdownSend() error {
	return statusToError(C.magnetk_stream_shutdown_send(s.handle))
}

// Close aborts the stream in both directions.
func (s *Stream) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	C.magnetk_stream_close(s.handle)
}
