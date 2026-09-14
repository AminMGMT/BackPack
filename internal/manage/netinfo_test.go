package manage

import (
	"net"
	"testing"
)

// What counts as "this server's public address".
//
// The bug this guards against reported a server as being in Baku on another
// operator's network, because the only source consulted was an echo service —
// which describes where the host's outbound traffic comes out, not what the
// host is.
func TestWhatCountsAsPublic(t *testing.T) {
	public := []string{
		"31.171.101.107",
		"8.8.8.8",
		"1.1.1.1",
		"2a01:4f8:c17:b8f::1",
	}
	notPublic := []string{
		"127.0.0.1",    // loopback
		"::1",          // loopback
		"10.0.0.5",     // RFC 1918
		"172.16.0.1",   // RFC 1918
		"192.168.1.10", // RFC 1918
		"169.254.1.1",  // link-local
		"fe80::1",      // link-local
		"fd00::1",      // unique local
		"0.0.0.0",      // unspecified
		"224.0.0.1",    // multicast
		// Carrier-grade NAT. net.IP.IsPrivate does not cover this range, and it
		// is the one that looks public enough to be reported and is not.
		"100.64.0.1",
		"100.100.50.1",
		"100.127.255.255",
	}

	for _, s := range public {
		if !globallyRoutable(net.ParseIP(s)) {
			t.Errorf("%s should count as a public address", s)
		}
	}
	for _, s := range notPublic {
		if globallyRoutable(net.ParseIP(s)) {
			t.Errorf("%s must not be reported as this server's public address", s)
		}
	}
	if globallyRoutable(nil) {
		t.Error("a nil address must not count as public")
	}
}

// 100.64.0.0/10 ends at 100.127.255.255, so both ends of the bound have to be
// checked. Matching on the first octet alone would throw away a /8 of ordinary
// public space.
func TestCGNATBoundIsNotJustTheFirstOctet(t *testing.T) {
	for _, s := range []string{"100.63.255.255", "100.128.0.1"} {
		if !globallyRoutable(net.ParseIP(s)) {
			t.Errorf("%s is outside 100.64.0.0/10 and should count as public", s)
		}
	}
}

// With one candidate there is nothing to decide. With several and no tunnel
// bound to any of them the choice has to at least be stable — a public address
// that changes between polls is its own bug report.
func TestPickLocalIsStable(t *testing.T) {
	if got := pickLocal([]string{"5.6.7.8"}); got != "5.6.7.8" {
		t.Fatalf("pickLocal with one candidate = %q, want 5.6.7.8", got)
	}

	candidates := []string{"5.6.7.8", "9.10.11.12"}
	first := pickLocal(candidates)
	for i := 0; i < 5; i++ {
		if got := pickLocal(candidates); got != first {
			t.Fatalf("pickLocal returned %q then %q for the same input", first, got)
		}
	}
}

// A wildcard bind names no address, so it must not be treated as one: a tunnel
// on 0.0.0.0 says nothing about which of several public IPs is this server's.
func TestBoundServerHostsIgnoresWildcards(t *testing.T) {
	bound := boundServerHosts()
	for _, wildcard := range []string{"0.0.0.0", "::"} {
		if bound[wildcard] {
			t.Errorf("%s was recorded as a bound address", wildcard)
		}
	}
}

// localPublicIPs must only ever return addresses it would itself call public,
// and must not mix the families. Whatever this machine happens to have, both
// hold.
func TestLocalPublicIPsAreSortedAndOfOneFamily(t *testing.T) {
	for _, v4 := range []bool{true, false} {
		got := localPublicIPs(v4)
		for i, s := range got {
			ip := net.ParseIP(s)
			if ip == nil {
				t.Errorf("localPublicIPs(%v) returned %q, which is not an address", v4, s)
				continue
			}
			if !globallyRoutable(ip) {
				t.Errorf("localPublicIPs(%v) returned %s, which is not public", v4, s)
			}
			if isV4 := ip.To4() != nil; isV4 != v4 {
				t.Errorf("localPublicIPs(%v) returned %s, which is the other family", v4, s)
			}
			if i > 0 && got[i-1] > s {
				t.Errorf("localPublicIPs(%v) is not sorted: %s came before %s", v4, got[i-1], s)
			}
		}
	}
}
