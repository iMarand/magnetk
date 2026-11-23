package relay

import (
	"context"
	"encoding/json"
	"net"
	"time"

	"github.com/iMarand/magnetk/go/pkg/protocol"
	"github.com/iMarand/magnetk/go/pkg/wire"
)

// LegacyFileService is the pipe service name under which nodes serve the
// Magnetk v1 file protocol (JSON manifest + binary chunks) for old clients.
const LegacyFileService = "mk.file.v1"

func (s *Server) acceptLegacy() {
	defer s.wg.Done()
	for {
		c, err := s.legacy.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			default:
				time.Sleep(10 * time.Millisecond)
				continue
			}
		}
		go s.handleLegacy(c)
	}
}

// handleLegacy speaks Magnetk v1 framing to old clients (JS SDK): identity,
// file lookup, heartbeat and proxying a download through a relayed pipe.
func (s *Server) handleLegacy(c net.Conn) {
	defer c.Close()
	s.st.legacyConns.Add(1)
	r := protocol.NewFrameReader(c)
	w := protocol.NewFrameWriter(c)
	for {
		c.SetReadDeadline(time.Now().Add(2 * time.Minute))
		f, err := r.ReadFrame()
		if err != nil {
			return
		}
		switch f.Type {
		case protocol.MsgGetIdentity:
			w.WriteJSON(protocol.MsgIdentityRes, protocol.IdentityResponsePayload{PeerID: s.cfg.ID, Multiaddrs: []string{}})

		case protocol.MsgHeartbeat:
			w.WriteJSON(protocol.MsgHeartbeatAck, protocol.HeartbeatPayload{Timestamp: time.Now().Unix()})

		case protocol.MsgLookupPeer:
			var req protocol.LookupPeerPayload
			if json.Unmarshal(f.Payload, &req) != nil {
				w.WriteJSON(protocol.MsgError, protocol.ErrorPayload{Code: 400, Message: "invalid payload"})
				continue
			}
			res := s.findFile(wire.FileFind{Hash: req.FileHash})
			out := protocol.LookupResultPayload{Found: len(res.Providers) > 0, Seeds: []protocol.SeedInfo{}}
			for _, p := range res.Providers {
				seed := protocol.SeedInfo{PeerID: p.NodeID, Addresses: p.Addrs}
				if len(p.Addrs) > 0 {
					seed.TransportAddr = p.Addrs[0]
				}
				out.Seeds = append(out.Seeds, seed)
			}
			w.WriteJSON(protocol.MsgLookupResult, out)

		case protocol.MsgProxyConnect:
			var req protocol.ProxyConnectPayload
			if json.Unmarshal(f.Payload, &req) != nil {
				w.WriteJSON(protocol.MsgError, protocol.ErrorPayload{Code: 400, Message: "invalid proxy payload"})
				continue
			}
			ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
			st, err := s.openPipeTo(ctx, "legacy:"+c.RemoteAddr().String(), req.PeerID, LegacyFileService, req.FileHash, true)
			cancel()
			if err != nil {
				w.WriteJSON(protocol.MsgError, protocol.ErrorPayload{Code: 404, Message: "provider unreachable: " + err.Error()})
				continue
			}
			w.WriteJSON(protocol.MsgProxyReady, protocol.ProxyReadyPayload{Success: true, Message: "connected through relay " + s.cfg.ID})
			c.SetReadDeadline(time.Time{})
			s.st.pipesTotal.Add(1)
			s.pipes.Add(1)
			done := make(chan struct{}, 2)
			go func() {
				bp := s.bufPool.Get().(*[]byte)
				n, _ := copyBuf(st, c, *bp)
				s.bufPool.Put(bp)
				s.st.pipeBytes.Add(uint64(n))
				st.CloseWrite()
				done <- struct{}{}
			}()
			go func() {
				bp := s.bufPool.Get().(*[]byte)
				n, _ := copyBuf(c, st, *bp)
				s.bufPool.Put(bp)
				s.st.pipeBytes.Add(uint64(n))
				if tc, ok := c.(interface{ CloseWrite() error }); ok {
					tc.CloseWrite()
				}
				done <- struct{}{}
			}()
			<-done
			<-done
			s.pipes.Add(-1)
			st.Close()
			return

		default:
			w.WriteJSON(protocol.MsgError, protocol.ErrorPayload{Code: 400, Message: "unsupported message type"})
		}
	}
}
