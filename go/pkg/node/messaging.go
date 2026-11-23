package node

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iMarand/magnetk/go/pkg/wire"
)

// Kind of an incoming message.
type Kind uint8

const (
	KindDirect   Kind = 1 // sent to this node with Send
	KindPublish  Kind = 2 // received through a subscription
	KindRequest  Kind = 3 // a request; answer with Respond
	KindPresence Kind = 4 // a node joined/left (Subject = "join"/"leave", From = node)
)

func (k Kind) String() string {
	switch k {
	case KindDirect:
		return "direct"
	case KindPublish:
		return "publish"
	case KindRequest:
		return "request"
	case KindPresence:
		return "presence"
	}
	return "unknown"
}

// Message is an incoming message.
type Message struct {
	Kind     Kind
	ID       uint64
	From     string // sender node id
	Subject  string // topic (publish/topic request) or presence event
	Data     []byte
	Datagram bool // arrived as an unreliable datagram

	node    *Node
	reqID   uint64
	replied atomic.Bool
}

// IsRequest reports whether the message expects a response.
func (m *Message) IsRequest() bool { return m.Kind == KindRequest }

// Respond answers a request.
func (m *Message) Respond(data []byte) error { return m.respond(wire.ReplyOK, data) }

// RespondError answers a request with an error message.
func (m *Message) RespondError(msg string) error { return m.respond(wire.ReplyError, []byte(msg)) }

func (m *Message) respond(status uint8, data []byte) error {
	if m.Kind != KindRequest || m.node == nil {
		return ErrNotRequest
	}
	if !m.replied.CompareAndSwap(false, true) {
		return errors.New("magnetk: request already answered")
	}
	r := wire.Reply{ID: m.reqID, To: m.From, Status: status, Payload: data}
	ctx, cancel := m.node.withTimeout(context.Background())
	defer cancel()
	_, err := m.node.send(ctx, r.Encode())
	return err
}

// ===== receiving =====

func (n *Node) toInbox(m *Message) {
	if !n.offer(n.inbox, m, &n.inboxSlow, nil) {
		n.stats.dropped.Add(1)
	}
}

// offer queues m on ch. When ch is full it waits up to SlowConsumerWait so
// bursts are absorbed without loss; a consumer that stays full is marked
// slow and messages are dropped (without waiting) until it has drained to
// half its capacity. Waiting stalls this relay connection's reader, which
// is the backpressure that keeps a burst lossless.
func (n *Node) offer(ch chan *Message, m *Message, slow *atomic.Bool, done <-chan struct{}) bool {
	select {
	case ch <- m:
		return true
	default:
	}
	if slow.Load() {
		if len(ch) > cap(ch)/2 {
			return false
		}
		slow.Store(false)
	}
	t := time.NewTimer(n.cfg.SlowConsumerWait)
	defer t.Stop()
	select {
	case ch <- m:
		return true
	case <-t.C:
		slow.Store(true)
		n.log.Printf("[node] consumer full for %v: dropping until it catches up", n.cfg.SlowConsumerWait)
		return false
	case <-done:
		return false
	case <-n.ctx.Done():
		return false
	}
}

func (n *Node) deliver(d wire.Deliver, datagram bool) {
	m := &Message{Kind: Kind(d.Kind), ID: d.ID, From: d.From, Subject: d.Subject, Data: d.Payload, Datagram: datagram || d.Flags&wire.FlagDatagram != 0, node: n, reqID: d.ReqID}
	switch m.Kind {
	case KindPublish:
		if n.dispatchSubs(m) {
			return
		}
	case KindRequest:
		if m.Subject != "" && n.dispatchSubs(m) {
			return
		}
		n.mu.RLock()
		h := n.reqHandler
		n.mu.RUnlock()
		if h != nil && m.Subject == "" {
			go runHandler(h, m)
			return
		}
	}
	n.toInbox(m)
}

func runHandler(h func(*Message) ([]byte, error), m *Message) {
	defer func() {
		if r := recover(); r != nil {
			m.RespondError(fmt.Sprint("handler panic: ", r))
		}
	}()
	out, err := h(m)
	if m.replied.Load() {
		return
	}
	if err != nil {
		m.RespondError(err.Error())
		return
	}
	m.Respond(out)
}

// dispatchSubs hands a message to every matching subscription with a
// handler or channel. It returns false when only inbox subscriptions match.
func (n *Node) dispatchSubs(m *Message) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	handled := false
	for s := range n.subs {
		if !wire.MatchTopic(s.Pattern, m.Subject) {
			continue
		}
		if s.ch == nil {
			continue // inbox subscription
		}
		handled = true
		if !n.offer(s.ch, m, &s.slow, s.done) {
			s.dropped.Add(1)
			n.stats.dropped.Add(1)
		}
		if m.Kind == KindRequest {
			break // a request is answered once
		}
	}
	return handled
}

// Receive returns the next message from the inbox: direct messages,
// requests without a handler, publications for subscriptions without a
// handler, and presence events.
func (n *Node) Receive(ctx context.Context) (*Message, error) {
	select {
	case m := <-n.inbox:
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-n.ctx.Done():
		return nil, ErrClosed
	}
}

// Messages exposes the inbox as a channel.
func (n *Node) Messages() <-chan *Message { return n.inbox }

// OnRequest sets the handler for requests addressed to this node (not to a
// topic). The returned bytes are sent back; an error is sent as a remote error.
func (n *Node) OnRequest(h func(*Message) ([]byte, error)) {
	n.mu.Lock()
	n.reqHandler = h
	n.mu.Unlock()
}

// ===== subscriptions =====

// Subscription is an active subscription.
type Subscription struct {
	Pattern string
	Queue   string

	n       *Node
	handler func(*Message)
	ch      chan *Message
	done    chan struct{}
	once    sync.Once
	dropped atomic.Uint64
	slow    atomic.Bool
}

// Dropped returns how many messages were dropped because the handler was slow.
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// Unsubscribe stops the subscription.
func (s *Subscription) Unsubscribe() error {
	n := s.n
	n.mu.Lock()
	_, ok := n.subs[s]
	delete(n.subs, s)
	still := false
	for other := range n.subs {
		if other.Pattern == s.Pattern {
			still = true
		}
	}
	n.mu.Unlock()
	s.stop()
	if !ok || still {
		return nil
	}
	ctx, cancel := n.withTimeout(context.Background())
	defer cancel()
	_, err := n.send(ctx, wire.EncodeUnsub(s.Pattern))
	if errors.Is(err, ErrNotConnected) || errors.Is(err, ErrClosed) {
		return nil // nothing to undo on the relay
	}
	return err
}

// UnsubscribePattern removes every subscription of this node for pattern.
func (n *Node) UnsubscribePattern(ctx context.Context, pattern string) error {
	n.mu.RLock()
	var subs []*Subscription
	for s := range n.subs {
		if s.Pattern == pattern {
			subs = append(subs, s)
		}
	}
	n.mu.RUnlock()
	if len(subs) == 0 {
		return fmt.Errorf("magnetk: not subscribed to %q", pattern)
	}
	var first error
	for _, s := range subs {
		if err := s.Unsubscribe(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (s *Subscription) stop() {
	s.once.Do(func() { close(s.done) })
}

// Subscribe subscribes to a topic pattern ("a.b", "a.*", "a.>").
// handler runs in its own goroutine, one message at a time, in order.
// With a nil handler, messages go to Receive/Messages.
func (n *Node) Subscribe(ctx context.Context, pattern string, handler func(*Message)) (*Subscription, error) {
	return n.QueueSubscribe(ctx, pattern, "", handler)
}

// QueueSubscribe subscribes as a member of a queue group: each message is
// delivered to only one member of the group (load balancing).
func (n *Node) QueueSubscribe(ctx context.Context, pattern, queue string, handler func(*Message)) (*Subscription, error) {
	if !wire.ValidPattern(pattern) {
		return nil, fmt.Errorf("magnetk: invalid subscription pattern %q", pattern)
	}
	s := &Subscription{Pattern: pattern, Queue: queue, n: n, handler: handler, done: make(chan struct{})}
	if handler != nil {
		s.ch = make(chan *Message, n.cfg.SubBuffer)
		go func() {
			for {
				select {
				case m := <-s.ch:
					func() {
						defer func() { recover() }()
						handler(m)
					}()
				case <-s.done:
					return
				}
			}
		}()
	}
	n.mu.Lock()
	n.subs[s] = struct{}{}
	n.mu.Unlock()
	// If disconnected, attach() sends the subscription on (re)connect.
	n.mu.RLock()
	rc := n.cur
	n.mu.RUnlock()
	if rc != nil {
		m := wire.Sub{Topic: pattern, Queue: queue}
		if err := rc.w.Send(ctx, m.Encode()); err != nil && !errors.Is(err, wire.ErrWriterClosed) {
			s.Unsubscribe()
			return nil, err
		}
		// Active at the relay when we return (a reconnect re-subscribes anyway).
		if err := n.Flush(ctx); err != nil && !errors.Is(err, ErrDisconnected) && !errors.Is(err, ErrNotConnected) {
			s.Unsubscribe()
			return nil, err
		}
	}
	return s, nil
}

// HandleRequests serves requests sent to a topic. Several nodes serving the
// same topic share the load (one queue group per topic).
func (n *Node) HandleRequests(ctx context.Context, topic string, h func(*Message) ([]byte, error)) (*Subscription, error) {
	if !wire.ValidTopic(topic) {
		return nil, fmt.Errorf("magnetk: invalid service topic %q", topic)
	}
	return n.QueueSubscribe(ctx, topic, "svc:"+topic, func(m *Message) {
		if m.Kind == KindRequest {
			go runHandler(h, m)
		}
	})
}

// ===== sending =====

// SendOption modifies Send / Publish.
type SendOption func(*sendOpts)

type sendOpts struct {
	ack      bool
	store    bool
	datagram bool
}

// WithReceipt waits for the relay to confirm delivery (or storage) and
// returns ErrNoRoute / ErrDropped otherwise.
func WithReceipt() SendOption { return func(o *sendOpts) { o.ack = true } }

// WithStore keeps the message at the relay (bounded, with TTL) if the
// receiver is offline, and delivers it when it reconnects.
func WithStore() SendOption { return func(o *sendOpts) { o.store = true } }

// Unreliable sends the message as a QUIC datagram: lowest latency, no
// retransmission, may be lost or reordered. Falls back to the reliable path
// when datagrams are unavailable or the message is too large.
func Unreliable() SendOption { return func(o *sendOpts) { o.datagram = true } }

func applyOpts(opts []SendOption) sendOpts {
	var o sendOpts
	for _, f := range opts {
		f(&o)
	}
	return o
}

// Send sends a direct message to a node.
func (n *Node) Send(ctx context.Context, to string, data []byte, opts ...SendOption) error {
	o := applyOpts(opts)
	id := n.nextID.Add(1)
	var flags uint8
	if o.store {
		flags |= wire.FlagStore
	}
	if o.datagram && !o.ack && !o.store {
		m := wire.Send{ID: id, To: to, Payload: data}
		if n.trySendDatagram(m.Encode()) {
			return nil
		}
	}
	if !o.ack {
		m := wire.Send{ID: id, To: to, Flags: flags, Payload: data}
		_, err := n.send(ctx, m.Encode())
		return err
	}
	flags |= wire.FlagAck
	ch := make(chan wire.Ack, 1)
	n.acks.Store(id, ch)
	defer n.acks.Delete(id)
	ctx, cancel := n.withTimeout(ctx)
	defer cancel()
	m := wire.Send{ID: id, To: to, Flags: flags, Payload: data}
	if _, err := n.send(ctx, m.Encode()); err != nil {
		return err
	}
	select {
	case a := <-ch:
		return ackErr(a)
	case <-ctx.Done():
		return ErrTimeout
	case <-n.ctx.Done():
		return ErrClosed
	}
}

func ackErr(a wire.Ack) error {
	switch a.Status {
	case wire.AckDelivered, wire.AckQueued:
		return nil
	case wire.AckNoRoute:
		return ErrNoRoute
	case wire.AckDropped:
		return ErrDropped
	case 255:
		return ErrDisconnected
	default:
		return fmt.Errorf("magnetk: rejected: %s", a.Detail)
	}
}

func (n *Node) trySendDatagram(frame []byte) bool {
	n.mu.RLock()
	rc := n.cur
	n.mu.RUnlock()
	if rc == nil || rc.conn.Protocol() != "quic" {
		return false
	}
	if max := rc.conn.MaxDatagram(); max == 0 || len(frame) > max {
		return false
	}
	if rc.conn.SendDatagram(frame) != nil {
		return false
	}
	n.stats.sent.Add(1)
	return true
}

// Publish sends data to every subscriber of topic.
func (n *Node) Publish(ctx context.Context, topic string, data []byte, opts ...SendOption) error {
	if !wire.ValidTopic(topic) {
		return fmt.Errorf("magnetk: invalid topic %q", topic)
	}
	o := applyOpts(opts)
	m := wire.Pub{ID: n.nextID.Add(1), Topic: topic, Payload: data}
	frame := m.Encode()
	if o.datagram && n.trySendDatagram(frame) {
		return nil
	}
	_, err := n.send(ctx, frame)
	return err
}

// Request sends a request to a node and waits for its response.
func (n *Node) Request(ctx context.Context, to string, data []byte) ([]byte, error) {
	return n.request(ctx, wire.Req{To: to, Payload: data})
}

// RequestTopic sends a request to one of the nodes serving topic
// (see HandleRequests) and waits for its response.
func (n *Node) RequestTopic(ctx context.Context, topic string, data []byte) ([]byte, error) {
	if !wire.ValidTopic(topic) {
		return nil, fmt.Errorf("magnetk: invalid topic %q", topic)
	}
	return n.request(ctx, wire.Req{Topic: topic, Payload: data})
}

func (n *Node) request(ctx context.Context, r wire.Req) ([]byte, error) {
	ctx, cancel := n.withTimeout(ctx)
	defer cancel()
	r.ID = n.nextID.Add(1)
	if dl, ok := ctx.Deadline(); ok {
		if ms := int64(timeUntilMs(dl)); ms > 0 {
			r.TimeoutMs = uint32(ms)
		}
	}
	ch := make(chan wire.Reply, 1)
	n.pending.Store(r.ID, ch)
	defer n.pending.Delete(r.ID)
	if _, err := n.send(ctx, r.Encode()); err != nil {
		return nil, err
	}
	select {
	case rep := <-ch:
		switch rep.Status {
		case wire.ReplyOK:
			return rep.Payload, nil
		case wire.ReplyNoRoute:
			return nil, fmt.Errorf("%w: %s", ErrNoRoute, rep.Payload)
		case 255:
			return nil, ErrDisconnected
		default:
			return nil, &RemoteError{Msg: string(rep.Payload)}
		}
	case <-ctx.Done():
		return nil, ErrTimeout
	case <-n.ctx.Done():
		return nil, ErrClosed
	}
}

// WatchPresence turns presence events (KindPresence messages) on or off.
func (n *Node) WatchPresence(ctx context.Context, on bool) error {
	n.mu.Lock()
	n.cfg.WatchPresence = on
	n.mu.Unlock()
	_, err := n.send(ctx, wire.EncodeWatch(on))
	return err
}
