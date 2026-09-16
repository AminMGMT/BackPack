package network

import "net"

// NewSpoofPacketConn opens the spoof carrier as a bare net.PacketConn.
//
// This file held the WireGuard-pipe mode: a raw UDP flow relayed over the spoof
// channel instead of a KCP tunnel, on the reasoning that WireGuard brings its
// own encryption and its own handling of a lossy link, so stacking KCP under it
// only doubles the reliability layer. That mode was removed. Its buffer sizer
// and its relay loop were not, and stayed here as an export surface with no
// consumer — the direct tunnel carries a whole private network now, which is
// what the pipe was reached for.
//
// realPeer is the peer's real address: the server's for a client, the client's
// for a server.
func NewSpoofPacketConn(server bool, token string, c SpoofCarrier, realPeer net.IP) (net.PacketConn, error) {
	if server {
		return newSpoofServerConn(c.spoofOpts(token, realPeer))
	}
	return newSpoofClientConn(c.spoofOpts(token, realPeer))
}
