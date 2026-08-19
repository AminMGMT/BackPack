package l3

import (
	"net"
	"testing"
)

// The receive path runs once per packet, so what it allocates is multiplied by
// the packet rate. These pin the two places that mattered.

func TestSameAddrDoesNotAllocate(t *testing.T) {
	a := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 9), Port: 9000}
	b := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 9), Port: 9000}

	if n := testing.AllocsPerRun(100, func() { _ = sameAddr(a, b) }); n != 0 {
		t.Errorf("comparing two peers allocates %v times per packet — String() is back", n)
	}
}

func TestSameAddrIsCorrect(t *testing.T) {
	udp := func(ip string, port int) net.Addr { return &net.UDPAddr{IP: net.ParseIP(ip), Port: port} }

	if !sameAddr(udp("203.0.113.9", 9000), udp("203.0.113.9", 9000)) {
		t.Error("the same peer was seen as different")
	}
	if sameAddr(udp("203.0.113.9", 9000), udp("203.0.113.9", 9001)) {
		t.Error("a different port was seen as the same peer")
	}
	if sameAddr(udp("203.0.113.9", 9000), udp("198.51.100.4", 9000)) {
		t.Error("a different address was seen as the same peer")
	}
	// A peer that moves must be noticed, or the tunnel keeps answering to where
	// it used to be.
	if sameAddr(udp("203.0.113.9", 9000), &net.IPAddr{IP: net.ParseIP("203.0.113.9")}) {
		t.Error("two different address kinds compared equal")
	}
	if !sameAddr(nil, nil) || sameAddr(nil, udp("203.0.113.9", 9000)) {
		t.Error("nil handling is wrong")
	}
	// IPv4 written two ways is still one address.
	if !sameAddr(&net.UDPAddr{IP: net.ParseIP("203.0.113.9"), Port: 1},
		&net.UDPAddr{IP: net.IPv4(203, 0, 113, 9), Port: 1}) {
		t.Error("the same IPv4 address in two representations compared unequal")
	}
}
