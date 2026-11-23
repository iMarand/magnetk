package relay

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/iMarand/magnetk/go/pkg/wire"
)

type frameReader struct{ r *bufio.Reader }

func newFrameReader(r io.Reader) *frameReader {
	return &frameReader{r: bufio.NewReaderSize(r, 64<<10)}
}

func (f *frameReader) next(max int) (uint8, []byte, error) {
	return wire.ReadFrame(f.r, max)
}

// dispatch handles one frame from a node or a relay link.
func (s *Server) dispatch(ss *session, typ uint8, body []byte) error {
	fromLink := ss.link != nil

	if ss.limiter != nil && (typ == wire.TSend || typ == wire.TPub || typ == wire.TReq) && !ss.limiter.allow() {
		s.st.rateLimited.Add(1)
		ss.sendError(wire.ErrCodeRateLimited, 0, "rate limit exceeded")
		return nil
	}

	switch typ {
	case wire.TSend:
		m, err := wire.DecodeSend(body)
		if err != nil {
			return err
		}
		if !fromLink {
			m.From = ss.id
		}
		s.routeSend(ss, m, fromLink)

	case wire.TPub:
		m, err := wire.DecodePub(body)
		if err != nil {
			return err
		}
		if !fromLink {
			m.From = ss.id
		}
		if !wire.ValidTopic(m.Topic) {
			ss.sendError(wire.ErrCodeBadRequest, m.ID, "invalid topic "+m.Topic)
			return nil
		}
		s.routePub(ss, wire.FwdPub{ID: m.ID, From: m.From, Topic: m.Topic, Flags: m.Flags, Payload: m.Payload}, fromLink)

	case wire.TFwdPub:
		if !fromLink {
			return fmt.Errorf("forwarded publish from a node")
		}
		m, err := wire.DecodeFwdPub(body)
		if err != nil {
			return err
		}
		s.routePub(ss, m, true)

	case wire.TSub:
		if fromLink {
			return nil
		}
		m, err := wire.DecodeSub(body)
		if err != nil {
			return err
		}
		s.subscribe(ss, m)

	case wire.TUnsub:
		if fromLink {
			return nil
		}
		topic, err := wire.DecodeUnsub(body)
		if err != nil {
			return err
		}
		s.unsubscribe(ss, topic)

	case wire.TReq:
		m, err := wire.DecodeReq(body)
		if err != nil {
			return err
		}
		if !fromLink {
			m.From = ss.id
		}
		s.routeReq(ss, m, fromLink)

	case wire.TReply:
		m, err := wire.DecodeReply(body)
		if err != nil {
			return err
		}
		if !fromLink {
			m.From = ss.id
		}
		s.routeReply(m, fromLink)

	case wire.TAck:
		if !fromLink {
			return nil
		}
		m, err := wire.DecodeAck(body)
		if err != nil {
			return err
		}
		s.mu.RLock()
		target := s.sessions[m.To]
		s.mu.RUnlock()
		if target != nil {
			target.enqueue(m.Encode())
		}

	case wire.TPing:
		v, err := wire.DecodeU64(body)
		if err != nil {
			return err
		}
		ss.enqueue(wire.EncodePong(v))

	case wire.TPong:
		// keep-alive answer; nothing to do

	case wire.TWatch:
		d := wire.NewDec(body)
		on := d.U8() != 0
		if d.Err() != nil {
			return d.Err()
		}
		ss.watch.Store(on)

	case wire.TLookup:
		m, err := wire.DecodeLookup(body)
		if err != nil {
			return err
		}
		res := wire.LookupRes{ID: m.ID, Nodes: s.lookup(m.Kind, m.Key)}
		ss.enqueue(res.Encode())

	case wire.TStatsReq:
		id, err := wire.DecodeU64(body)
		if err != nil {
			return err
		}
		js, _ := json.Marshal(s.Stats())
		ss.enqueue(wire.EncodeStatsRes(id, js))

	case wire.TFilePublish:
		m, err := wire.DecodeFilePublish(body)
		if err != nil {
			return err
		}
		if fromLink {
			s.files.publish(m, ss.link.relayID)
			return nil
		}
		m.Provider = ss.id
		if len(m.Hash) != 64 {
			ss.sendError(wire.ErrCodeBadRequest, 0, "file hash must be 64 hex characters")
			return nil
		}
		s.files.publish(m, "")
		s.forwardToLinks(m.Encode())

	case wire.TFileUnpublish:
		m, err := wire.DecodeFileUnpublish(body)
		if err != nil {
			return err
		}
		if !fromLink {
			m.Provider = ss.id
		}
		s.files.unpublish(m.Provider, m.Hash)
		if !fromLink {
			s.forwardToLinks(m.Encode())
		}

	case wire.TFileFind:
		m, err := wire.DecodeFileFind(body)
		if err != nil {
			return err
		}
		ss.enqueue(s.findFile(m).Encode())

	case wire.TRouteAdd, wire.TRouteDel, wire.TInterestAdd, wire.TInterestDel:
		if !fromLink {
			return nil
		}
		return s.handleLinkFrame(ss.link, typ, body)

	case wire.TBye:
		go ss.close("goodbye")

	default:
		return fmt.Errorf("unknown frame type 0x%02x", typ)
	}
	return nil
}

func (s *Server) forwardToLinks(frame []byte) {
	s.mu.RLock()
	for _, l := range s.links {
		l.sess.enqueue(frame)
	}
	s.mu.RUnlock()
}

// ===== direct messages =====

func (s *Server) routeSend(from *session, m wire.Send, fromLink bool) {
	s.st.msgs.Add(1)
	wantAck := m.Flags&wire.FlagAck != 0
	ack := func(status uint8, detail string) {
		if !wantAck {
			return
		}
		a := wire.Ack{ID: m.ID, To: m.From, Status: status, Detail: detail}
		from.enqueue(a.Encode())
	}

	s.mu.RLock()
	target := s.sessions[m.To]
	var remote *remoteNode
	if target == nil && !fromLink {
		remote = s.routes[m.To]
	}
	s.mu.RUnlock()

	switch {
	case target != nil:
		d := wire.Deliver{ID: m.ID, Kind: wire.KindDirect, From: m.From, Flags: m.Flags &^ (wire.FlagAck | wire.FlagForwarded), Payload: m.Payload}
		if target.deliver(d.Encode(), m.Flags&wire.FlagDatagram != 0) {
			ack(wire.AckDelivered, "")
		} else {
			ack(wire.AckDropped, "receiver is not keeping up")
		}
	case remote != nil:
		m.Flags |= wire.FlagForwarded
		if !remote.link.sess.enqueue(m.Encode()) {
			ack(wire.AckDropped, "relay link congested")
		}
		// The remote relay acknowledges directly to the sender.
	case m.Flags&wire.FlagStore != 0:
		d := wire.Deliver{ID: m.ID, Kind: wire.KindDirect, From: m.From, Flags: m.Flags &^ (wire.FlagAck | wire.FlagForwarded), Payload: m.Payload}
		if s.offline.put(m.To, d.Encode()) {
			s.st.stored.Add(1)
			ack(wire.AckQueued, "stored until the node connects")
		} else {
			ack(wire.AckDropped, "offline storage full")
		}
	default:
		s.st.noRoute.Add(1)
		ack(wire.AckNoRoute, "node "+m.To+" is not connected")
	}
}

// ===== publish / subscribe =====

func (s *Server) subscribe(ss *session, m wire.Sub) {
	if !wire.ValidPattern(m.Topic) {
		ss.sendError(wire.ErrCodeBadRequest, 0, "invalid subscription pattern "+m.Topic)
		return
	}
	ss.subsMu.Lock()
	if _, exists := ss.subs[m.Topic]; !exists && len(ss.subs) >= s.cfg.MaxSubsPerSession {
		ss.subsMu.Unlock()
		ss.sendError(wire.ErrCodeOverloaded, 0, "too many subscriptions")
		return
	}
	ss.subs[m.Topic] = m.Queue
	ss.subsMu.Unlock()
	if s.subs.add(ss, m.Topic, m.Queue) {
		s.forwardToLinks(wire.EncodeStrList(wire.TInterestAdd, []string{m.Topic}))
	}
}

func (s *Server) unsubscribe(ss *session, topic string) {
	ss.subsMu.Lock()
	delete(ss.subs, topic)
	ss.subsMu.Unlock()
	if s.subs.remove(ss, topic) {
		s.forwardToLinks(wire.EncodeStrList(wire.TInterestDel, []string{topic}))
	}
}

func (s *Server) routePub(from *session, m wire.FwdPub, fromLink bool) {
	s.st.pubs.Add(1)
	served := make(map[string]bool, len(m.ServedQueues))
	for _, q := range m.ServedQueues {
		served[q] = true
	}
	targets, groups := s.subs.match(m.Topic, served)

	if len(targets) > 0 {
		d := wire.Deliver{ID: m.ID, Kind: wire.KindPublish, From: m.From, Subject: m.Topic, Flags: m.Flags &^ (wire.FlagAck | wire.FlagForwarded), Payload: m.Payload}
		frame := d.Encode()
		dgram := m.Flags&wire.FlagDatagram != 0
		// A publisher receives its own message only if it is subscribed.
		for _, t := range targets {
			t.deliver(frame, dgram)
		}
		s.st.fanout.Add(uint64(len(targets)))
	}

	if fromLink {
		return
	}
	// Forward to relays that have interested subscribers.
	s.mu.RLock()
	var fwdLinks []*link
	for _, l := range s.links {
		if l.interested(m.Topic) {
			fwdLinks = append(fwdLinks, l)
		}
	}
	s.mu.RUnlock()
	if len(fwdLinks) > 0 {
		m.Flags |= wire.FlagForwarded
		m.ServedQueues = append(m.ServedQueues, groups...)
		frame := m.Encode()
		for _, l := range fwdLinks {
			l.sess.enqueue(frame)
		}
	}
}

// ===== request / reply =====

func (s *Server) routeReq(from *session, m wire.Req, fromLink bool) {
	s.st.requests.Add(1)
	fail := func(status uint8, msg string) {
		r := wire.Reply{ID: m.ID, From: m.To, To: m.From, Status: status, Payload: []byte(msg)}
		if r.From == "" {
			r.From = m.Topic
		}
		from.enqueue(r.Encode())
	}
	d := wire.Deliver{ID: m.ID, Kind: wire.KindRequest, From: m.From, Subject: m.Topic, ReqID: m.ID, Flags: m.Flags &^ wire.FlagForwarded, Payload: m.Payload}

	if m.To != "" {
		s.mu.RLock()
		target := s.sessions[m.To]
		var remote *remoteNode
		if target == nil && !fromLink {
			remote = s.routes[m.To]
		}
		s.mu.RUnlock()
		switch {
		case target != nil:
			if !target.enqueue(d.Encode()) {
				fail(wire.ReplyError, "receiver is not keeping up")
			}
		case remote != nil:
			m.Flags |= wire.FlagForwarded
			remote.link.sess.enqueue(m.Encode())
		default:
			fail(wire.ReplyNoRoute, "node "+m.To+" is not connected")
		}
		return
	}

	if !wire.ValidTopic(m.Topic) {
		fail(wire.ReplyError, "request needs a node id or a valid topic")
		return
	}
	if target := s.subs.pickOne(m.Topic); target != nil {
		if !target.enqueue(d.Encode()) {
			fail(wire.ReplyError, "service is not keeping up")
		}
		return
	}
	if !fromLink {
		s.mu.RLock()
		var via *link
		for _, l := range s.links {
			if l.interested(m.Topic) {
				via = l
				break
			}
		}
		s.mu.RUnlock()
		if via != nil {
			m.Flags |= wire.FlagForwarded
			via.sess.enqueue(m.Encode())
			return
		}
	}
	fail(wire.ReplyNoRoute, "no subscriber for "+m.Topic)
}

func (s *Server) routeReply(m wire.Reply, fromLink bool) {
	s.mu.RLock()
	target := s.sessions[m.To]
	var remote *remoteNode
	if target == nil && !fromLink {
		remote = s.routes[m.To]
	}
	s.mu.RUnlock()
	switch {
	case target != nil:
		target.enqueue(m.Encode())
	case remote != nil:
		remote.link.sess.enqueue(m.Encode())
	}
}

// ===== lookup =====

func (s *Server) lookup(kind uint8, key string) []wire.NodeInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []wire.NodeInfo
	switch kind {
	case wire.LookupNode:
		if ss := s.sessions[key]; ss != nil {
			out = append(out, ss.info)
		} else if r := s.routes[key]; r != nil {
			out = append(out, r.info)
		}
	case wire.LookupPrefix:
		for id, ss := range s.sessions {
			if strings.HasPrefix(id, key) {
				out = append(out, ss.info)
				if len(out) >= 1000 {
					return out
				}
			}
		}
		for id, r := range s.routes {
			if _, local := s.sessions[id]; !local && strings.HasPrefix(id, key) {
				out = append(out, r.info)
				if len(out) >= 1000 {
					return out
				}
			}
		}
	}
	return out
}

func (s *Server) nodeInfo(id string) (wire.NodeInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if ss := s.sessions[id]; ss != nil {
		return ss.info, true
	}
	if r := s.routes[id]; r != nil {
		return r.info, true
	}
	return wire.NodeInfo{}, false
}

func (s *Server) findFile(m wire.FileFind) *wire.FileProviders {
	res := &wire.FileProviders{ID: m.ID, Hash: m.Hash}
	e, providers := s.files.find(m.Hash)
	if e == nil {
		return res
	}
	res.Name, res.Size, res.ChunkSize, res.Root = e.name, e.size, e.chunkSize, e.root
	for _, p := range providers {
		if info, ok := s.nodeInfo(p); ok {
			res.Providers = append(res.Providers, info)
		}
	}
	return res
}
