package acceptloop

import (
	"net"
	"testing"
)

// The pre-authentication budget: a host may hold only so many connections that
// have not yet proved anything, and the listener only so many in all.
func TestThePreauthGateBoundsStrangersPerHostAndInAll(t *testing.T) {
	g := Gate{PerHost: 2, Total: 3}
	a := &net.TCPAddr{IP: net.ParseIP("203.0.113.1"), Port: 1}
	b := &net.TCPAddr{IP: net.ParseIP("203.0.113.2"), Port: 1}

	var leaves []func()
	for i := 0; i < 2; i++ {
		leave, ok := g.Enter(a)
		if !ok {
			t.Fatalf("host a refused at %d", i)
		}
		leaves = append(leaves, leave)
	}
	if _, ok := g.Enter(a); ok {
		t.Fatal("host a was let past its own bound")
	}
	leave, ok := g.Enter(b)
	if !ok {
		t.Fatal("host b was refused for host a's connections")
	}
	if _, ok := g.Enter(b); ok {
		t.Fatal("the listener was let past its total bound")
	}
	leave()
	leaves[0]()
	if _, ok := g.Enter(a); !ok {
		t.Fatal("a place given back was not reusable")
	}
}

// A flood that fills the gate does not shut out the host that has proved the
// token: its re-dials and pool refills pass without taking a place.
func TestAProvenHostPassesAFullGate(t *testing.T) {
	g := Gate{PerHost: 1, Total: 1}
	stranger := &net.TCPAddr{IP: net.ParseIP("198.51.100.9"), Port: 1}
	client := &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 1}

	if _, ok := g.Enter(stranger); !ok {
		t.Fatal("the stranger's first connection was refused")
	}
	if _, ok := g.Enter(client); ok {
		t.Fatal("setup: the gate was not full")
	}
	g.Prove(&net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 40000})
	for i := 0; i < 5; i++ {
		if _, ok := g.Enter(client); !ok {
			t.Fatalf("the proven host was refused at %d", i)
		}
	}
}

// One IPv6 subscriber is one host: every address in its /64 shares one
// allowance.
func TestAnIPv6SubscriberCountsOnce(t *testing.T) {
	g := Gate{PerHost: 2, Total: 100}
	for i, a := range []string{"2001:db8:1:2::1", "2001:db8:1:2::ffff"} {
		if _, ok := g.Enter(&net.TCPAddr{IP: net.ParseIP(a), Port: 1}); !ok {
			t.Fatalf("refused at %d", i)
		}
	}
	if _, ok := g.Enter(&net.TCPAddr{IP: net.ParseIP("2001:db8:1:2:dead::9"), Port: 1}); ok {
		t.Fatal("a third address in the same /64 was let past the per-host bound")
	}
	if _, ok := g.Enter(&net.TCPAddr{IP: net.ParseIP("2001:db8:1:3::1"), Port: 1}); !ok {
		t.Fatal("another /64 was refused")
	}
}
