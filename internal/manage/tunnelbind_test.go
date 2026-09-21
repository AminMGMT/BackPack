package manage

import (
	"net"
	"strings"
	"testing"
)

// The control port takes an address as well as a port.
//
// Reported from a two-address Iran server: the operator wanted the control
// channel on IP_A:443 — so it blends in as HTTPS like everything else — and a
// forwarded port on IP_B:443 for the users. The forwarded ports have taken an
// address for a while; the control port could not, because every path that
// built a BindAddr wrote 0.0.0.0 and joined the port to it. Both ends therefore
// asked for 0.0.0.0:443 and the second got `bind: address already in use`.
func TestTheTunnelPortAcceptsAnAddressAndStillAcceptsABarePort(t *testing.T) {
	for _, tc := range []struct {
		in       string
		wantHost string
		wantPort string
		why      string
	}{
		{"443", "", "443", "a bare port is the form every existing config uses"},
		{" 443 ", "", "443", "surrounding space is the operator's, not the value's"},
		{":443", "", "443", "a port with the host left off, as listen addresses are spelled"},
		{"0.0.0.0:443", "0.0.0.0", "443", "the wildcard said out loud"},
		{"85.10.11.51:443", "85.10.11.51", "443", "the case this exists for"},
		{"127.0.0.1:8443", "127.0.0.1", "8443", "loopback only, for a tunnel reached over SSH"},
		{"[::]:443", "::", "443", "the v6 wildcard, which takes v4 too on a dual-stack host"},
		{"[2a01:4f8::1]:443", "2a01:4f8::1", "443", "a v6 literal, bracketed"},
		{"1.2.3.4:1", "1.2.3.4", "1", "the bottom of the port range"},
		{"1.2.3.4:65535", "1.2.3.4", "65535", "the top of it"},
	} {
		got, err := parseTunnelBind(tc.in)
		if err != nil {
			t.Errorf("parseTunnelBind(%q) failed: %v — %s", tc.in, err, tc.why)
			continue
		}
		if got.Host != tc.wantHost || got.Port != tc.wantPort {
			t.Errorf("parseTunnelBind(%q) = host %q port %q, want %q/%q — %s",
				tc.in, got.Host, got.Port, tc.wantHost, tc.wantPort, tc.why)
		}
	}
}

// What must be refused, and refused with a sentence that says what to write.
func TestAnUnusableTunnelPortIsRefused(t *testing.T) {
	for _, tc := range []struct {
		in       string
		contains string
		why      string
	}{
		{"", "no tunnel port", "an empty field is not a port"},
		{"0", "between 1 and 65535", "port 0 asks the kernel to choose, which a peer cannot dial"},
		{"65536", "between 1 and 65535", "one past the end"},
		{"-1", "between 1 and 65535", "not a port"},
		{"abc", "between 1 and 65535", "not a number"},
		{"1.2.3.4:0", "between 1 and 65535", "a valid address does not excuse the port"},
		{"1.2.3.4:abc", "between 1 and 65535", "same, the other way round"},
		{"1.2.3.4", "between 1 and 65535", "an address with no port is not a port"},
		{"example.com:443", "not an IP address", "a bind address is an interface, not a name to resolve"},
		{"localhost:443", "not an IP address", "even the obvious name — say 127.0.0.1"},
		{"2a01:4f8::1:443", "[address]:port", "a bare v6 literal has to be bracketed to be readable"},
		{"999.1.1.1:443", "not an IP address", "four octets, one of them impossible"},
	} {
		got, err := parseTunnelBind(tc.in)
		if err == nil {
			t.Errorf("parseTunnelBind(%q) was accepted as host %q port %q — %s",
				tc.in, got.Host, got.Port, tc.why)
			continue
		}
		if !strings.Contains(err.Error(), tc.contains) {
			t.Errorf("parseTunnelBind(%q) said %q, which does not mention %q — %s",
				tc.in, err, tc.contains, tc.why)
		}
	}
}

// The IPv6 switch decides the wildcard family and nothing else. An operator who
// named an address has already answered that question, and overriding it with a
// checkbox would be ignoring what they typed.
func TestTheIPv6SwitchOnlyDecidesTheWildcard(t *testing.T) {
	bare, err := parseTunnelBind("443")
	if err != nil {
		t.Fatalf("parseTunnelBind: %v", err)
	}
	if got := bare.Addr(false); got != "0.0.0.0:443" {
		t.Errorf("a bare port without the switch gave %q, want 0.0.0.0:443 — the behaviour every existing config has", got)
	}
	if got := bare.Addr(true); got != "[::]:443" {
		t.Errorf("a bare port with the switch gave %q, want [::]:443", got)
	}

	pinned, err := parseTunnelBind("85.10.11.51:443")
	if err != nil {
		t.Fatalf("parseTunnelBind: %v", err)
	}
	for _, ipv6 := range []bool{false, true} {
		if got := pinned.Addr(ipv6); got != "85.10.11.51:443" {
			t.Errorf("a named address with ipv6=%v gave %q; the switch must not move it", ipv6, got)
		}
	}
}

// The panel shows the port back the way it was typed, so accepting the field
// unchanged cannot quietly widen a pinned tunnel to every interface. A wildcard
// has no address to show.
func TestTheBindHostIsReportedOnlyWhenTheTunnelIsPinned(t *testing.T) {
	for _, tc := range []struct{ addr, want string }{
		{"0.0.0.0:443", ""},
		{"[::]:443", ""},
		{":443", ""},
		{"85.10.11.51:443", "85.10.11.51"},
		{"[2a01:4f8::1]:443", "2a01:4f8::1"},
		{"nonsense", ""},
	} {
		if got := bindHostOf(tc.addr); got != tc.want {
			t.Errorf("bindHostOf(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

// The scenario from the report, run for real.
//
// Two addresses, one port number, both bound at once. 127.0.0.1 and 127.0.0.2
// are both this machine's, which makes the two-address case reproducible
// anywhere rather than only on a server that has two public addresses.
func TestTwoAddressesCanHoldTheSamePortNumber(t *testing.T) {
	const port = "0" // let the kernel pick, then reuse the number it gave

	first, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		t.Skipf("cannot listen on 127.0.0.1 here: %v", err)
	}
	defer first.Close()
	_, chosen, err := net.SplitHostPort(first.Addr().String())
	if err != nil {
		t.Fatalf("unexpected listener address %q", first.Addr())
	}

	// The same port on the other address: this is what used to fail, because
	// both ends were forced onto 0.0.0.0.
	second, err := net.Listen("tcp", net.JoinHostPort("127.0.0.2", chosen))
	if err != nil {
		t.Skipf("this host cannot bind 127.0.0.2 (%v); the dual-address case needs two local addresses", err)
	}
	defer second.Close()

	// And the check the CLI makes before writing must agree: pinned to the
	// second address it is busy, pinned to a third it is not.
	if !TunnelPortInUse("tcp", net.JoinHostPort("127.0.0.2", chosen)) {
		t.Error("the address that is genuinely held was reported free")
	}
	if TunnelPortInUse("tcp", net.JoinHostPort("127.0.0.3", chosen)) {
		t.Error("an address nothing holds was reported busy — this is the refusal " +
			"the whole feature exists to get past")
	}
	// The wildcard is busy, correctly: it would need every address including
	// the two that are taken.
	if !TunnelPortInUse("tcp", chosen) {
		t.Error("a bare port must still mean every interface, and two of them are held")
	}
}

// A bare port keeps meaning what it always meant, including to the in-use check
// that takes a full address now.
func TestABarePortStillMeansEveryInterface(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Skipf("cannot open a listener here: %v", err)
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("unexpected listener address %q", ln.Addr())
	}
	if !TunnelPortInUse("tcp", port) {
		t.Errorf("port %s has a wildcard listener on it and was reported free", port)
	}
	if !TunnelPortInUse("tcp", ":"+port) {
		t.Errorf("%q is the same question spelled differently and gave a different answer", ":"+port)
	}
}

// The panel's create form takes the same two forms the CLI does.
func TestTheCreateFormTakesAnAddressOnTheTunnelPort(t *testing.T) {
	base := NewTunnel{
		Role: "server", Transport: "tcp", Name: "iran-a",
		Token: "a-token-long-enough-to-be-accepted", Ports: "8080",
	}

	pinned := base
	pinned.TunnelPort = "85.10.11.51:443"
	s, err := specFromNew(pinned)
	if err != nil {
		t.Fatalf("a pinned tunnel port was refused by the create form: %v", err)
	}
	if s.BindAddr != "85.10.11.51:443" {
		t.Errorf("BindAddr = %q, want 85.10.11.51:443", s.BindAddr)
	}

	// The switch must not move an address the operator named.
	pinned.IPv6 = true
	if s, err = specFromNew(pinned); err != nil || s.BindAddr != "85.10.11.51:443" {
		t.Errorf("with the IPv6 switch on, BindAddr = %q (err %v); a named address is not the wildcard", s.BindAddr, err)
	}

	// And the old shape is untouched.
	plain := base
	plain.TunnelPort = "443"
	if s, err = specFromNew(plain); err != nil || s.BindAddr != "0.0.0.0:443" {
		t.Errorf("a bare port gave BindAddr %q (err %v), want 0.0.0.0:443", s.BindAddr, err)
	}
	plain.IPv6 = true
	if s, err = specFromNew(plain); err != nil || s.BindAddr != "[::]:443" {
		t.Errorf("a bare port with IPv6 gave BindAddr %q (err %v), want [::]:443", s.BindAddr, err)
	}
}

// A client binds nothing, so an address in its tunnel-port field is aimed at
// the wrong setting and is told so rather than quietly ignored.
func TestAClientIsToldItsTunnelPortIsNotABindAddress(t *testing.T) {
	_, err := specFromNew(NewTunnel{
		Role: "client", Transport: "tcp", Name: "kharej-a",
		Token:      "a-token-long-enough-to-be-accepted",
		ServerAddr: "203.0.113.9", TunnelPort: "85.10.11.51:443",
	})
	if err == nil {
		t.Fatal("a client accepted an address as its tunnel port; that field is the port on the SERVER")
	}
	if !strings.Contains(err.Error(), "binds nothing") {
		t.Errorf("the refusal does not explain why: %v", err)
	}

	// The ordinary client shape still works.
	s, err := specFromNew(NewTunnel{
		Role: "client", Transport: "tcp", Name: "kharej-a",
		Token:      "a-token-long-enough-to-be-accepted",
		ServerAddr: "203.0.113.9", TunnelPort: "443",
	})
	if err != nil {
		t.Fatalf("an ordinary client was refused: %v", err)
	}
	if s.RemoteAddr != "203.0.113.9:443" {
		t.Errorf("RemoteAddr = %q, want 203.0.113.9:443", s.RemoteAddr)
	}
}

// Two servers on one port number are still refused when they would contend for
// it, and allowed when they would not. This is the decision that makes the
// dual-address setup legal, and it already worked — the check compares host and
// port together and treats a wildcard as covering everything. Pinned here so a
// later change to the input path cannot quietly make it unreachable again.
func TestTwoServersMayShareAPortOnDifferentAddresses(t *testing.T) {
	existing := []Tunnel{{Name: "control", Role: "server", Addr: "85.10.11.51:443"}}

	if why := clashAgainst("server", "85.10.11.61:443", "users", existing); why != "" {
		t.Errorf("two servers on the same port but different addresses were refused: %s", why)
	}
	if why := clashAgainst("server", "85.10.11.51:443", "users", existing); why == "" {
		t.Error("two servers on the same address and port were allowed; the second cannot bind")
	}
	// A wildcard covers every address, so it contends with the pinned one.
	if why := clashAgainst("server", "0.0.0.0:443", "users", existing); why == "" {
		t.Error("a wildcard alongside a pinned tunnel on the same port was allowed, " +
			"but the wildcard needs that address too")
	}
	if why := clashAgainst("server", "85.10.11.61:443", "users",
		[]Tunnel{{Name: "control", Role: "server", Addr: "0.0.0.0:443"}}); why == "" {
		t.Error("a pinned tunnel alongside a wildcard on the same port was allowed, the other way round")
	}
}

// A pinned tunnel whose address this machine does not hold is reported, on the
// one surface the panel, the CLI and the bot all read.
//
// The wizard warns when the address is typed and the engine's bind failure says
// it plainly, but neither reaches somebody who set the tunnel up from the panel
// or whose address went away afterwards.
func TestAPinnedAddressThisServerDoesNotHaveIsNoticed(t *testing.T) {
	// 203.0.113.0/24 is TEST-NET-3: reserved for documentation, so no machine
	// running this suite has one.
	if localAddrExists("203.0.113.7") {
		t.Skip("this machine somehow holds a TEST-NET-3 address")
	}
	// And the check must not cry wolf about an address that is real.
	if !localAddrExists("127.0.0.1") {
		t.Fatal("loopback was reported as not present on this machine")
	}
	// A malformed host is not an address at all, and must not read as one that
	// is merely absent.
	if localAddrExists("not-an-address") {
		t.Error("a string that is not an IP was reported as a local address")
	}
}
