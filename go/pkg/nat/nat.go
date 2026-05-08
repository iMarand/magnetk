// Package nat provides UPnP/NAT-PMP port mapping for Magnetk seeds.
//
// On startup the seed calls Open() to map TRANSPORT_PORT through the
// home router. This makes the seed reachable from the public internet
// without manual port forwarding, enabling true direct P2P transfers
// across NATs.
package nat

import (
	"context"
	"log"
	"net"
	"sync"
	"time"

	libp2pnat "github.com/libp2p/go-nat"
)

// Mapping is one mapped (protocol, port) pair on the gateway.
// Each Mapping owns a background goroutine that refreshes the lease.
type Mapping struct {
	nat          libp2pnat.NAT
	Protocol     string // "tcp" or "udp"
	InternalPort int
	ExternalPort int
	ExternalIP   net.IP
	desc         string
	stopRefresh  chan struct{}
	closeOnce    sync.Once
}

// Open discovers a UPnP/NAT-PMP gateway and maps both TCP and UDP for the
// given internal port. Returns one Mapping per protocol that succeeded.
//
// Discovery has a hard timeout so networks without UPnP do not block the
// caller. Each returned Mapping refreshes its lease in a background
// goroutine; callers should defer Close on each mapping.
func Open(internalPort int, description string) []*Mapping {
	discoverCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	n, err := libp2pnat.DiscoverGateway(discoverCtx)
	if err != nil || n == nil {
		log.Printf("[nat] No UPnP/NAT-PMP gateway discovered (normal on data-center / public IPs): %v", err)
		return nil
	}

	extIP, err := n.GetExternalAddress()
	if err != nil {
		log.Printf("[nat] Gateway found but external IP query failed: %v", err)
	}

	var out []*Mapping
	for _, proto := range []string{"tcp", "udp"} {
		mapCtx, mapCancel := context.WithTimeout(context.Background(), 5*time.Second)
		ext, err := n.AddPortMapping(mapCtx, proto, internalPort, description, time.Hour)
		mapCancel()
		if err != nil {
			log.Printf("[nat] AddPortMapping %s/%d failed: %v", proto, internalPort, err)
			continue
		}
		m := &Mapping{
			nat:          n,
			Protocol:     proto,
			InternalPort: internalPort,
			ExternalPort: ext,
			ExternalIP:   extIP,
			desc:         description,
			stopRefresh:  make(chan struct{}),
		}
		m.startRefresh()
		out = append(out, m)
		log.Printf("[nat] Mapped %s/%d -> %s:%d (lease 1h, refresh 30m)",
			proto, internalPort, ipString(extIP), ext)
	}
	return out
}

func (m *Mapping) startRefresh() {
	go func() {
		t := time.NewTicker(30 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-m.stopRefresh:
				return
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, err := m.nat.AddPortMapping(ctx, m.Protocol, m.InternalPort, m.desc, time.Hour)
				cancel()
				if err != nil {
					log.Printf("[nat] Refresh %s/%d failed: %v", m.Protocol, m.InternalPort, err)
				}
			}
		}
	}()
}

// Close stops the refresh loop and removes the port mapping from the gateway.
// Safe to call multiple times.
func (m *Mapping) Close() {
	m.closeOnce.Do(func() {
		close(m.stopRefresh)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := m.nat.DeletePortMapping(ctx, m.Protocol, m.InternalPort); err != nil {
			log.Printf("[nat] DeletePortMapping %s/%d failed: %v", m.Protocol, m.InternalPort, err)
		}
	})
}

func ipString(ip net.IP) string {
	if ip == nil {
		return "?"
	}
	return ip.String()
}
