package relay

import (
	"encoding/binary"

	"github.com/iMarand/magnetk/go/pkg/wire"
)

// datagramLoop handles messages a node sends as unreliable QUIC datagrams.
// Each datagram carries one complete SEND or PUB frame.
func (s *Server) datagramLoop(ss *session) {
	if ss.conn.Protocol() != "quic" {
		return
	}
	for {
		d, err := ss.conn.ReceiveDatagram(ss.ctx)
		if err != nil {
			return
		}
		if len(d) < wire.HeaderSize || int(binary.BigEndian.Uint32(d)) != len(d)-wire.HeaderSize {
			continue
		}
		ss.in.Add(1)
		if ss.limiter != nil && !ss.limiter.allow() {
			s.st.rateLimited.Add(1)
			continue
		}
		body := d[wire.HeaderSize:]
		switch d[4] {
		case wire.TSend:
			m, err := wire.DecodeSend(body)
			if err != nil {
				continue
			}
			m.From = ss.id
			m.Flags = (m.Flags | wire.FlagDatagram) &^ (wire.FlagAck | wire.FlagStore)
			s.routeSend(ss, m, false)
		case wire.TPub:
			m, err := wire.DecodePub(body)
			if err != nil || !wire.ValidTopic(m.Topic) {
				continue
			}
			s.routePub(ss, wire.FwdPub{ID: m.ID, From: ss.id, Topic: m.Topic, Flags: (m.Flags | wire.FlagDatagram) &^ wire.FlagAck, Payload: m.Payload}, false)
		}
	}
}

// deliver sends a frame to a node, as a datagram when the message arrived as
// one and the receiver can take it, otherwise on the reliable control stream.
func (ss *session) deliver(frame []byte, datagram bool) bool {
	if datagram && ss.link == nil {
		if max := ss.conn.MaxDatagram(); max > 0 && len(frame) <= max {
			if ss.conn.SendDatagram(frame) == nil {
				ss.out.Add(1)
				return true
			}
		}
	}
	return ss.enqueue(frame)
}
