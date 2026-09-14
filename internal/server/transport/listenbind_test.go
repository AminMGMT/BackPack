package transport

import (
	"net"
	"testing"
)

// The reason the bind address exists at all, proved against real sockets rather
// than against the parser's output.
//
// A multi-homed server has two public IPs and wants the control channel on one
// and an exposed port on the other, both on 443. Bound to the wildcard address
// those are the same socket and the second bind fails with "address already in
// use"; bound to an address each, they coexist. The loopback range stands in for
// the two public IPs, since all of 127/8 is local on Linux.
func TestTwoListenersShareAPortOnDifferentAddresses(t *testing.T) {
	const port = "35443" // above the privileged range, below the ephemeral one

	first, err := expandListenSpec("127.0.0.2:" + port)
	if err != nil {
		t.Fatalf("could not expand the first mapping: %v", err)
	}
	second, err := expandListenSpec("127.0.0.3:" + port)
	if err != nil {
		t.Fatalf("could not expand the second mapping: %v", err)
	}

	l1, err := net.Listen("tcp", first[0].addr)
	if err != nil {
		t.Skipf("cannot bind %s on this host: %v", first[0].addr, err)
	}
	defer l1.Close()

	l2, err := net.Listen("tcp", second[0].addr)
	if err != nil {
		t.Fatalf("binding %s failed while %s was already bound, which is the "+
			"port conflict the bind address exists to avoid: %v",
			second[0].addr, first[0].addr, err)
	}
	defer l2.Close()

	if got := l1.Addr().String(); got != first[0].addr {
		t.Errorf("first listener bound %s, want %s", got, first[0].addr)
	}
	if got := l2.Addr().String(); got != second[0].addr {
		t.Errorf("second listener bound %s, want %s", got, second[0].addr)
	}
}

// A range carrying a bind address has to produce listeners on that address, not
// on the wildcard. This is the case that used to kill the tunnel at startup:
// every transport tested for "-" before it looked for a host, so the whole
// "127.0.0.2:35500-35502" string reached strconv.Atoi and failed.
func TestARangeKeepsItsBindAddress(t *testing.T) {
	listens, err := expandListenSpec("127.0.0.2:35500-35502")
	if err != nil {
		t.Fatalf("a range with a bind address did not parse: %v", err)
	}
	if len(listens) != 3 {
		t.Fatalf("got %d listeners, want 3", len(listens))
	}

	for _, l := range listens {
		ln, err := net.Listen("tcp", l.addr)
		if err != nil {
			t.Skipf("cannot bind %s on this host: %v", l.addr, err)
		}
		host, _, err := net.SplitHostPort(ln.Addr().String())
		ln.Close()
		if err != nil {
			t.Fatalf("unexpected listener address %s: %v", ln.Addr(), err)
		}
		if host != "127.0.0.2" {
			t.Errorf("listener for %s bound host %s, want 127.0.0.2", l.addr, host)
		}
	}
}
