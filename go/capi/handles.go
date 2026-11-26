package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/iMarand/magnetk/go/pkg/chat"
	"github.com/iMarand/magnetk/go/pkg/files"
	"github.com/iMarand/magnetk/go/pkg/node"
	"github.com/iMarand/magnetk/go/pkg/relay"
)

// Status codes returned to C (kept in sync with magnetk.h).
const (
	mkOK              = 0
	mkErr             = -1
	mkErrTimeout      = -2
	mkErrClosed       = -3
	mkErrNoRoute      = -4
	mkErrUnauthorized = -5
	mkErrNotConnected = -6
	mkErrRemote       = -7
	mkErrRejected     = -8
	mkErrNoProviders  = -9
	mkErrInvalid      = -10
	mkErrDropped      = -11
)

func codeOf(err error) int {
	if err == nil {
		return mkOK
	}
	var re *node.RemoteError
	var pr *node.PipeRejectedError
	switch {
	case errors.Is(err, node.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return mkErrTimeout
	case errors.Is(err, node.ErrClosed):
		return mkErrClosed
	case errors.Is(err, node.ErrNoRoute):
		return mkErrNoRoute
	case errors.Is(err, node.ErrUnauthorized):
		return mkErrUnauthorized
	case errors.Is(err, node.ErrNotConnected), errors.Is(err, node.ErrDisconnected):
		return mkErrNotConnected
	case errors.As(err, &re):
		return mkErrRemote
	case errors.As(err, &pr):
		return mkErrRejected
	case errors.Is(err, node.ErrNoProviders):
		return mkErrNoProviders
	case errors.Is(err, node.ErrDropped):
		return mkErrDropped
	}
	return mkErr
}

// handle table: C code only ever sees opaque uint64 ids.
type table[T any] struct {
	mu   sync.RWMutex
	m    map[uint64]T
	next atomic.Uint64
}

func newTable[T any]() *table[T] { return &table[T]{m: make(map[uint64]T)} }

func (t *table[T]) add(v T) uint64 {
	id := t.next.Add(1)
	t.mu.Lock()
	t.m[id] = v
	t.mu.Unlock()
	return id
}

func (t *table[T]) get(id uint64) (T, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	v, ok := t.m[id]
	return v, ok
}

func (t *table[T]) remove(id uint64) (T, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.m[id]
	delete(t.m, id)
	return v, ok
}

type nodeHandle struct {
	n *node.Node

	mu       sync.Mutex
	nextReq  uint64
	requests map[uint64]*node.Message // pending requests awaiting mk_respond

	chat  *chat.Box  // secure chat ("chat": true)
	chatq []chat.Msg // decoded chat events waiting for mk_recv
}

type downloadHandle struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	progress files.Progress
	result   *files.Result
	err      error
}

var (
	nodes     = newTable[*nodeHandle]()
	pipes     = newTable[*node.Pipe]()
	downloads = newTable[*downloadHandle]()
	relays    = newTable[*relay.Server]()
)
