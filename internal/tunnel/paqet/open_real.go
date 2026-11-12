package paqet

import (
	"context"
	"fmt"
	"net"

	"github.com/backpack/backpack/internal/utils/network"
)

// openReal opens libpcap, claims the ports and installs the firewall rules.
func (e *Engine) openReal(ctx context.Context) (*wireSet, error) {
	if !PcapAvailable {
		return nil, ErrNoPcap
	}
	_, portStr, _ := net.SplitHostPort(e.cfg.Addr)
	var port uint16
	fmt.Sscanf(portStr, "%d", &port)

	var peerIP net.IP
	if p := e.peer.Load(); p != nil {
		peerIP = p.IP
	}
	path, err := network.DiscoverPckPath(peerIP, e.cfg.Interface, e.cfg.GatewayMAC)
	if err != nil {
		return nil, err
	}
	lease, err := network.LeasePckPorts(e.cfg.Token, e.cfg.Server, port, path, peerIP)
	if err != nil {
		return nil, err
	}
	lo, hi := lease.PortRange()
	filter := CaptureFilter(e.cfg.Server, lo, hi, path.LocalIP, peerIP, port)
	cap, inj, err := OpenPcap(path, filter, e.p.PcapBuf, e.cfg.Flags, senders(e.p))
	if err != nil {
		lease.Close()
		return nil, err
	}
	wire := NewWire(cap, inj, path.LocalIP)
	ws := &wireSet{wire: wire, lease: lease, local: path.LocalIP, listenPort: port}
	ws.close = func() { wire.Close(); lease.Close() }
	if e.cfg.Server {
		ws.serverSocket, err = wire.Open(port, nil)
		if err != nil {
			ws.close()
			return nil, err
		}
	}
	hop := "learned from each client's segments"
	if path.NextHop != nil {
		hop = path.NextHop.String()
	} else if !path.Ethernet {
		hop = "none (no link-layer header)"
	}
	e.log.Infof("pck: %s on %s (%s), next hop %s, capture ring %d KiB, filter %q",
		map[bool]string{true: "listening", false: "dialling"}[e.cfg.Server], path.Iface, path.LocalIP,
		hop, e.p.PcapBuf>>10, filter)
	return ws, nil
}

// senders is how many send handles an engine opens: one per KCP session the
// preset holds, up to MaxSenders, so each session's segments leave in parallel
// with the others' (see OpenPcap). The server takes the same figure from its
// own preset, which the setup link makes the Iran server's: it answers that
// many sessions per client.
func senders(p Params) int { return max(1, min(p.Conns, MaxSenders)) }
