package health

import (
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/utils/network"
)

func okPath(peer net.IP, iface, mac string) (*network.PckPath, error) {
	mac2, _ := net.ParseMAC("aa:bb:cc:dd:ee:ff")
	p := &network.PckPath{Iface: "eth0", LocalIP: net.IPv4(10, 0, 0, 4), Ethernet: true}
	if peer != nil {
		p.NextHop = mac2
	}
	return p, nil
}

func wholeGuard(string) network.PckGuardStatus {
	return network.PckGuardStatus{Iptables: true, Rules: network.PckGuardWant}
}

func hostFor() pckHost {
	return pckHost{pcap: true, path: okPath, guard: wholeGuard,
		resolve: func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("203.0.113.9")}, nil }}
}

func find(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check among %+v", name, checks)
	return Check{}
}

// A PCK tunnel is diagnosed by what it needs from the machine — libpcap, the
// firewall guard and a gateway — and never by an interface of its own.
func TestAPckTunnelChecksNoTunInterface(t *testing.T) {
	l := config.L3Config{Mode: "dial", Carrier: "pck", Addr: "203.0.113.9:9000", Token: "tok"}
	checks := pckChecksOn("g", l, hostFor())
	for _, c := range checks {
		if c.Name == "Interface" || c.Level != CheckOK {
			t.Errorf("unexpected check on a healthy machine: %+v", c)
		}
	}
	gw := find(t, checks, "Gateway")
	if !strings.Contains(gw.Detail, "aa:bb:cc:dd:ee:ff") || !strings.Contains(gw.Detail, "eth0") {
		t.Errorf("the gateway check does not say what it found: %q", gw.Detail)
	}
	find(t, checks, "Packet capture")
	find(t, checks, "Firewall guard")
}

// A build with no libpcap cannot run the carrier, and that one failure is the
// whole answer: the other checks would only repeat it.
func TestAPckBuildWithoutLibpcapFailsOnItsOwn(t *testing.T) {
	h := hostFor()
	h.pcap = false
	checks := pckChecksOn("g", config.L3Config{Mode: "dial", Addr: "203.0.113.9:9000", Token: "t"}, h)
	if len(checks) != 1 || checks[0].Level != CheckFail || !strings.Contains(checks[0].Detail, "libpcap") {
		t.Fatalf("want one libpcap failure, got %+v", checks)
	}
}

func TestTheFirewallGuardIsReadFromTheTables(t *testing.T) {
	l := config.L3Config{Mode: "dial", Addr: "203.0.113.9:9000", Token: "tok"}
	cases := map[string]struct {
		st   network.PckGuardStatus
		want CheckLevel
		text string
	}{
		"whole":       {network.PckGuardStatus{Iptables: true, Rules: 3}, CheckOK, "RSTs"},
		"partial":     {network.PckGuardStatus{Iptables: true, Rules: 1}, CheckWarn, "1 of 3"},
		"none":        {network.PckGuardStatus{Iptables: true}, CheckFail, "none of the tunnel's firewall rules"},
		"no iptables": {network.PckGuardStatus{}, CheckFail, "iptables is not installed"},
		"cannot read": {network.PckGuardStatus{Iptables: true, Err: errors.New("permission denied")}, CheckWarn, "permission denied"},
	}
	for name, c := range cases {
		h := hostFor()
		h.guard = func(string) network.PckGuardStatus { return c.st }
		got := pckGuardCheck("g", l, h)
		if got.Level != c.want || !strings.Contains(got.Detail, c.text) {
			t.Errorf("%s: %+v, want level %v mentioning %q", name, got, c.want, c.text)
		}
		if c.want != CheckOK && got.Fix == "" {
			t.Errorf("%s: a failing guard check has no fix", name)
		}
	}
}

// The gateway is the one thing the dialling side cannot do without, and the
// engine's own refusal names it; the check carries that through with a fix.
func TestAMissingGatewayIsNamed(t *testing.T) {
	h := hostFor()
	h.path = func(net.IP, string, string) (*network.PckPath, error) {
		return nil, errors.New("pck: the next hop's MAC address on eth0 is not in the neighbour table")
	}
	c := pckPathCheck("g", config.L3Config{Mode: "dial", Addr: "203.0.113.9:9000"}, false, h)
	if c.Level != CheckFail || c.Name != "Gateway" || !strings.Contains(c.Fix, "ARP") {
		t.Fatalf("got %+v", c)
	}
}

func TestThePckPathCheckRefusesWhatCannotBeDialled(t *testing.T) {
	h := hostFor()
	cases := map[string]string{
		"[2001:db8::1]:9000": "IPv4 only",
		"nohostport":         "host:port",
	}
	for addr, want := range cases {
		c := pckPathCheck("g", config.L3Config{Mode: "dial", Addr: addr}, false, h)
		if c.Level != CheckFail || !strings.Contains(c.Detail, want) {
			t.Errorf("%s: %+v, want a failure mentioning %q", addr, c, want)
		}
	}
	h.resolve = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("2001:db8::1")}, nil }
	if c := pckPathCheck("g", config.L3Config{Mode: "dial", Addr: "v6only.example:9000"}, false, h); c.Level != CheckFail ||
		!strings.Contains(c.Detail, "no IPv4") {
		t.Errorf("a domain with only AAAA: %+v", c)
	}
	h.resolve = func(string) ([]net.IP, error) { return nil, errors.New("no such host") }
	if c := pckPathCheck("g", config.L3Config{Mode: "dial", Addr: "gone.example:9000"}, false, h); c.Level != CheckFail ||
		!strings.Contains(c.Detail, "does not resolve") {
		t.Errorf("a domain that does not resolve: %+v", c)
	}
}

// The listening side has no peer, so no gateway to look for: its path check
// asks for the interface alone and says the next hop is learned.
func TestTheKharejSidePathCheckLooksForNoGateway(t *testing.T) {
	var asked net.IP = net.IPv4(1, 1, 1, 1)
	h := hostFor()
	h.path = func(peer net.IP, iface, mac string) (*network.PckPath, error) {
		asked = peer
		return okPath(peer, iface, mac)
	}
	c := pckPathCheck("g", config.L3Config{Mode: "listen", Addr: "0.0.0.0:9000"}, true, h)
	if asked != nil || c.Level != CheckOK || c.Name != "Network path" || !strings.Contains(c.Detail, "learned") {
		t.Fatalf("peer %v, check %+v", asked, c)
	}
}
