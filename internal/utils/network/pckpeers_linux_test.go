//go:build linux

package network

import (
	"net"
	"testing"
)

// The pck carrier learns a peer from every segment aimed at its port, before
// anything is authenticated, so the table it keeps must not grow with the
// number of forged sources that can be thrown at it.
func TestThePckPeerTableIsBounded(t *testing.T) {
	c := &pckConn{peers: make(map[pckPeerKey]*pckPeer)}
	genuine := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 443}
	c.peerFor(genuine, false)
	c.peerFor(genuine, true) // the tunnel accepted it and answers it

	// A flood of forged sources, with the genuine peer silent throughout: an
	// idle real peer must survive it too, since its sequence state is what a
	// middlebox tracking the flow has seen.
	for i := 0; i < 20*pckMaxPeers; i++ {
		c.peerFor(&net.UDPAddr{IP: net.IPv4(10, byte(i>>16), byte(i>>8), byte(i)), Port: 1000 + i%50000}, false)
	}
	if n := len(c.peers); n > pckMaxPeers {
		t.Fatalf("the table holds %d peers; the cap is %d", n, pckMaxPeers)
	}
	var key pckPeerKey
	copy(key.ip[:], genuine.IP.To4())
	key.port = uint16(genuine.Port)
	if c.peers[key] == nil {
		t.Fatal("the peer the tunnel answers was evicted to make room for forged ones")
	}
}
