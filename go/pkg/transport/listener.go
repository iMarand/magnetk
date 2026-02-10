// Package transport provides the Go-side listener for accepting
// inbound MsQuic connections and streams.
package transport

/*
#include "transport.h"
#include <stdlib.h>

// CGo cannot call Go function pointers directly, so we use these C trampolines
// that are exported from listener_cgo.c (or we use the //export mechanism).

extern void goOnConnection(void* listener, void* conn, void* userData);
extern void goOnStream(void* conn, void* stream, void* userData);
extern void goOnData(void* stream, uint8_t* data, uint32_t length, int fin, void* userData);
extern void goOnClose(void* handle, int32_t status, void* userData);
*/
import "C"
import (
	"fmt"
	"sync"
	"unsafe"
)

// StreamHandler is called when a new stream is opened on a connection.
// The handler receives the stream and should manage it (typically in a goroutine).
type StreamHandler func(stream *Stream, conn *Connection)

// DataHandler is called when data is received on a stream.
type DataHandler func(data []byte, fin bool)

// Listener accepts incoming QUIC connections on the transport.
type Listener struct {
	handle    *C.magnet_listener_t
	transport *Transport
	mu        sync.Mutex
	closed    bool

	// Callbacks
	onStream StreamHandler
	streams  sync.Map // maps C stream pointer to *Stream
	conns    sync.Map // maps C conn pointer to *Connection
}

// Listen starts listening for incoming connections.
func (t *Transport) Listen(onStream StreamHandler) (*Listener, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, ErrClosed
	}
	t.mu.Unlock()

	l := &Listener{
		transport: t,
		onStream:  onStream,
	}

	// Store listener reference for CGo callbacks
	listenerID := registerListener(l)

	var cListener *C.magnet_listener_t
	status := C.magnet_listen(
		t.handle,
		C.magnet_on_connection_fn(C.goOnConnection),
		C.magnet_on_stream_fn(C.goOnStream),
		C.magnet_on_data_fn(C.goOnData),
		C.magnet_on_close_fn(C.goOnClose),
		unsafe.Pointer(listenerID),
		&cListener,
	)
	if err := statusToError(status); err != nil {
		unregisterListener(listenerID)
		return nil, fmt.Errorf("listen: %w", err)
	}

	l.handle = cListener
	return l, nil
}

// Close stops the listener.
func (l *Listener) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	C.magnet_listener_close(l.handle)
}

// ===== CGo callback registry =====
// We need a way to map C void* back to Go objects.
// This uses a simple ID → pointer map protected by a mutex.

var (
	listenerMu       sync.Mutex
	listenerRegistry         = make(map[uintptr]*Listener)
	listenerNextID   uintptr = 1
)

func registerListener(l *Listener) uintptr {
	listenerMu.Lock()
	defer listenerMu.Unlock()
	id := listenerNextID
	listenerNextID++
	listenerRegistry[id] = l
	return id
}

func getListener(id uintptr) *Listener {
	listenerMu.Lock()
	defer listenerMu.Unlock()
	return listenerRegistry[id]
}

func unregisterListener(id uintptr) {
	listenerMu.Lock()
	defer listenerMu.Unlock()
	delete(listenerRegistry, id)
}

// ===== CGo callback exports =====
// These are called from C when events occur.

//export goOnConnection
func goOnConnection(listener unsafe.Pointer, conn unsafe.Pointer, userData unsafe.Pointer) {
	id := uintptr(userData)
	l := getListener(id)
	if l == nil {
		return
	}

	goConn := &Connection{
		handle:    (*C.magnet_connection_t)(conn),
		transport: l.transport,
	}
	l.conns.Store(uintptr(conn), goConn)
}

//export goOnStream
func goOnStream(conn unsafe.Pointer, stream unsafe.Pointer, userData unsafe.Pointer) {
	id := uintptr(userData)
	l := getListener(id)
	if l == nil {
		return
	}

	// Look up the Go connection
	connVal, ok := l.conns.Load(uintptr(conn))
	if !ok {
		return
	}
	goConn := connVal.(*Connection)

	goStream := &Stream{
		handle: (*C.magnet_stream_t)(stream),
		conn:   goConn,
	}
	l.streams.Store(uintptr(stream), goStream)

	// Invoke the user's stream handler in a goroutine
	if l.onStream != nil {
		go l.onStream(goStream, goConn)
	}
}

//export goOnData
func goOnData(stream unsafe.Pointer, data *C.uint8_t, length C.uint32_t, fin C.int, userData unsafe.Pointer) {
	// Data is received — for sync operations this is handled in the C layer.
	// This callback is only fired for async mode. Since our Go bridge uses
	// sync recv, this is a no-op for now.
	_ = stream
	_ = data
	_ = length
	_ = fin
	_ = userData
}

//export goOnClose
func goOnClose(handle unsafe.Pointer, status C.int32_t, userData unsafe.Pointer) {
	_ = handle
	_ = status
	_ = userData
}
