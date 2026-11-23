package relay

import (
	"context"
	"crypto/subtle"
	"fmt"
	"sync"
	"time"

	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/wire"
)

// link is a connection to another relay of the federation.
type link struct {
	relayID  string
	outbound bool
	sess     *session

	mu       sync.RWMutex
	interest map[string]bool // patterns the remote relay has subscribers for
}

func (l *link) interested(topic string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.interest[topic] {
		return true
	}
	for p := range l.interest {
		if wire.MatchTopic(p, topic) {
			return true
		}
	}
	return false
}

// peerLoop keeps an outbound link to a configured peer relay alive.
func (s *Server) peerLoop(addr string) {
	defer s.wg.Done()
	backoff := time.Second
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}
		started := time.Now()
		err := s.dialPeer(addr)
		if s.ctx.Err() != nil {
			return
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second
		}
		wait := backoff
		if pe, ok := err.(*peerReject); ok && pe.code == wire.ErrCodeDuplicate {
			wait = 15 * time.Second // the peer already keeps a link to us
		} else if err != nil {
			s.log.Printf("[relay] link to %s: %v (retry in %v)", addr, err, wait)
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(wait):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

type peerReject struct {
	code uint16
	msg  string
}

func (e *peerReject) Error() string { return fmt.Sprintf("rejected (%d): %s", e.code, e.msg) }

func (s *Server) dialPeer(addr string) error {
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	c, err := s.ep.Dial(ctx, addr)
	if err != nil {
		return err
	}
	ctrl, err := c.OpenStream(ctx)
	if err != nil {
		c.Close()
		return err
	}
	hello := wire.Hello{Version: wire.Version, NodeID: s.cfg.ID, Token: s.cfg.ClusterSecret, Flags: wire.HelloRelayLink, Client: "relay"}
	if _, err := ctrl.Write(hello.Encode()); err != nil {
		c.Close()
		return err
	}
	ctrl.SetReadDeadline(time.Now().Add(15 * time.Second))
	typ, body, err := wire.ReadFrameExact(ctrl, 64<<10)
	ctrl.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		return err
	}
	if typ == wire.TError {
		e, _ := wire.DecodeError(body)
		c.Close()
		return &peerReject{e.Code, e.Msg}
	}
	if typ != wire.TWelcome {
		c.Close()
		return fmt.Errorf("unexpected frame 0x%02x", typ)
	}
	w, err := wire.DecodeWelcome(body)
	if err != nil || w.RelayID == "" {
		c.Close()
		return fmt.Errorf("bad welcome")
	}
	if w.RelayID == s.cfg.ID {
		c.Close()
		return &peerReject{wire.ErrCodeDuplicate, "peer is this relay"}
	}
	ss := s.newSession(c, ctrl, w.RelayID, s.cfg.MaxQueueBytes*4)
	ss.w.Start()
	l := &link{relayID: w.RelayID, outbound: true, sess: ss, interest: make(map[string]bool)}
	ss.link = l
	if !s.registerLink(l) {
		ss.close("duplicate link")
		return &peerReject{wire.ErrCodeDuplicate, "duplicate link"}
	}
	s.log.Printf("[relay] linked to relay %s at %s via %s", w.RelayID, addr, c.Protocol())
	s.runLink(l)
	return nil
}

// acceptLink handles an inbound HELLO from another relay.
func (s *Server) acceptLink(c netx.Conn, ctrl netx.Stream, hello wire.Hello) {
	if s.cfg.ClusterSecret == "" || subtle.ConstantTimeCompare([]byte(s.cfg.ClusterSecret), []byte(hello.Token)) != 1 {
		s.st.authFailures.Add(1)
		reject(ctrl, c, wire.ErrCodeUnauthorized, "relay links require a matching cluster secret")
		return
	}
	if !validNodeID(hello.NodeID) || hello.NodeID == s.cfg.ID {
		reject(ctrl, c, wire.ErrCodeBadRequest, "invalid relay id")
		return
	}
	// Tie-break when both relays dial each other: keep the link dialed by
	// the relay with the smaller id.
	s.mu.RLock()
	existing := s.links[hello.NodeID]
	s.mu.RUnlock()
	if existing != nil && existing.outbound && s.cfg.ID < hello.NodeID {
		reject(ctrl, c, wire.ErrCodeDuplicate, "link already established")
		return
	}

	ss := s.newSession(c, ctrl, hello.NodeID, s.cfg.MaxQueueBytes*4)
	l := &link{relayID: hello.NodeID, outbound: false, sess: ss, interest: make(map[string]bool)}
	ss.link = l
	w := wire.Welcome{Version: wire.Version, NodeID: hello.NodeID, RelayID: s.cfg.ID, Observed: c.RemoteAddr(), MaxFrame: uint32(s.cfg.MaxFrame), ServerTime: uint64(time.Now().UnixMilli())}
	if err := ss.w.TrySend(w.Encode()); err != nil {
		ss.close("handshake failed")
		return
	}
	ss.w.Start()
	if existing != nil {
		existing.sess.close("replaced by new link")
	}
	if !s.registerLink(l) {
		ss.close("duplicate link")
		return
	}
	s.log.Printf("[relay] accepted link from relay %s via %s", hello.NodeID, c.Protocol())
	s.runLink(l)
}

func (s *Server) registerLink(l *link) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.links[l.relayID]; old != nil && old != l {
		return false
	}
	s.links[l.relayID] = l
	return true
}

// runLink syncs state to the peer and serves the link until it closes.
func (s *Server) runLink(l *link) {
	ss := l.sess
	s.mu.RLock()
	nodes := make([]wire.NodeInfo, 0, len(s.sessions))
	for _, n := range s.sessions {
		nodes = append(nodes, n.info)
	}
	s.mu.RUnlock()
	for i := 0; i < len(nodes); i += 500 {
		end := i + 500
		if end > len(nodes) {
			end = len(nodes)
		}
		ss.enqueue(wire.EncodeRouteAdd(nodes[i:end]))
	}
	if pats := s.subs.patterns(); len(pats) > 0 {
		ss.enqueue(wire.EncodeStrList(wire.TInterestAdd, pats))
	}
	for _, fp := range s.files.localPublications() {
		ss.enqueue(fp.Encode())
	}

	go s.acceptPipes(ss)
	s.readLoop(ss)
}

func (s *Server) unregisterLink(l *link) {
	s.mu.Lock()
	if s.links[l.relayID] == l {
		delete(s.links, l.relayID)
	}
	var gone []string
	for id, r := range s.routes {
		if r.link == l {
			delete(s.routes, id)
			if _, local := s.sessions[id]; !local {
				gone = append(gone, id)
			}
		}
	}
	s.mu.Unlock()
	s.files.removeRelay(l.relayID)
	for _, id := range gone {
		s.broadcastPresence(wire.PresenceLeave, id, "", nil)
	}
}

func (s *Server) handleLinkFrame(l *link, typ uint8, body []byte) error {
	switch typ {
	case wire.TRouteAdd:
		nodes, err := wire.DecodeRouteAdd(body)
		if err != nil {
			return err
		}
		var joined []wire.NodeInfo
		s.mu.Lock()
		for _, n := range nodes {
			if !validNodeID(n.NodeID) {
				continue
			}
			if _, exists := s.routes[n.NodeID]; !exists {
				joined = append(joined, n)
			}
			n.Relay = l.relayID
			s.routes[n.NodeID] = &remoteNode{info: n, link: l}
		}
		s.mu.Unlock()
		for _, n := range joined {
			s.broadcastPresence(wire.PresenceJoin, n.NodeID, n.Meta, nil)
		}
	case wire.TRouteDel:
		ids, err := wire.DecodeStrList(body)
		if err != nil {
			return err
		}
		var gone []string
		s.mu.Lock()
		for _, id := range ids {
			if r := s.routes[id]; r != nil && r.link == l {
				delete(s.routes, id)
				gone = append(gone, id)
			}
		}
		s.mu.Unlock()
		for _, id := range gone {
			s.broadcastPresence(wire.PresenceLeave, id, "", nil)
		}
	case wire.TInterestAdd, wire.TInterestDel:
		pats, err := wire.DecodeStrList(body)
		if err != nil {
			return err
		}
		l.mu.Lock()
		for _, p := range pats {
			if typ == wire.TInterestAdd {
				l.interest[p] = true
			} else {
				delete(l.interest, p)
			}
		}
		l.mu.Unlock()
	}
	return nil
}
