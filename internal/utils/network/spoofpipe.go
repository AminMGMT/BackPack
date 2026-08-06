package network

import (
	"context"
	"net"
	"sync/atomic"
)

// The WireGuard-pipe mode carries a raw UDP flow over the spoof channel instead
// of a KCP tunnel. WireGuard already brings its own encryption and its own
// handling of a lossy link, so stacking KCP under it would only double the
// reliability layer and add latency; the pipe therefore strips all of that and
// relays datagrams directly, forging the source exactly as the tunnel does.
//
// One WireGuard socket multiplexes all of its peers, so a single pipe carries a
// whole WireGuard instance — the whole-device L3 VPN the tunnel's SOCKS proxy
// cannot give.

// NewSpoofPacketConn opens the spoof carrier as a bare net.PacketConn, for the
// pipe (and any caller that wants the forged-source datagram transport without
// KCP on top). realPeer is the peer's real address: the server's for a client,
// the client's for a server.
func NewSpoofPacketConn(server bool, token string, c SpoofCarrier, realPeer net.IP) (net.PacketConn, error) {
	if server {
		return newSpoofServerConn(token, c.Uplink, c.Downlink, realPeer, c.SrcIP, c.SrcPool, c.Interface)
	}
	return newSpoofClientConn(token, c.Uplink, c.Downlink, realPeer, c.SrcIP, c.SrcPool, c.Interface)
}

// SpoofPipeRelay shuttles datagrams between the spoof carrier and a local UDP
// socket until the context is cancelled or either side fails, and returns the
// reason. When learnPeer is set (the client, whose udp is a listener that
// WireGuard sends to), replies are addressed back to the last source seen;
// otherwise (the server, whose udp is dialled to the real WireGuard endpoint)
// the connected socket's Write is used.
func SpoofPipeRelay(ctx context.Context, spoof net.PacketConn, udp *net.UDPConn, learnPeer bool) error {
	errc := make(chan error, 2)
	var peer atomic.Pointer[net.UDPAddr]

	// udp (WireGuard) -> spoof (to the tunnel peer)
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, addr, err := udp.ReadFromUDP(buf)
			if err != nil {
				errc <- err
				return
			}
			if learnPeer && addr != nil {
				peer.Store(addr)
			}
			if _, err := spoof.WriteTo(buf[:n], nil); err != nil {
				errc <- err
				return
			}
		}
	}()

	// spoof (from the tunnel peer) -> udp (WireGuard)
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, _, err := spoof.ReadFrom(buf)
			if err != nil {
				errc <- err
				return
			}
			if learnPeer {
				if a := peer.Load(); a != nil {
					_, _ = udp.WriteToUDP(buf[:n], a)
				}
				continue
			}
			_, _ = udp.Write(buf[:n])
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errc:
		return err
	}
}
