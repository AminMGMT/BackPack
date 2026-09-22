package spec

import "testing"

// The transport predicates, and one of them has a bug behind it worth naming.
//
// IsDatagram used to answer "no" for a layer-3 tunnel, because it compared the
// bare carrier names and a layer-3 transport arrives as "l3/pck". The web panel
// then probed a pck tunnel with a TCP connect — against a carrier that has no
// socket to connect to — and read the inevitable failure as the tunnel being
// down. The Iran card went offline while the tunnel was carrying traffic and
// the kharej card stayed green, because only the dialling side runs that probe.
//
// So the prefixes are the part that matters here, not the list.
func TestWhatIsCarriedInDatagrams(t *testing.T) {
	for _, tc := range []struct {
		transport string
		want      bool
	}{
		{"udp", true}, {"kcp", true}, {"xdi", true}, {"quic", true}, {"pck", true},
		{"tcp", false}, {"tcpmux", false}, {"ws", false}, {"wss", false}, {"stealth", false},

		// Every layer-3 carrier is a datagram one and no reliable carrier can
		// ever be added, so the prefix is enough.
		{"l3/udp", true}, {"l3/pck", true}, {"l3/spoof", true}, {"l3/anything-later", true},

		// All four direct transports are streams over TCP, so a TCP probe is
		// exactly the right thing for them.
		{"direct/tcp", false}, {"direct/ws", false}, {"direct/stealth", false},

		{"", false},
	} {
		if got := IsDatagram(tc.transport); got != tc.want {
			t.Errorf("IsDatagram(%q) = %v, want %v", tc.transport, got, tc.want)
		}
	}
}

func TestTheTransportFamilies(t *testing.T) {
	// Every mux transport must also be a valid transport, or the wizard offers
	// tuning for a carrier that cannot be selected.
	for _, t2 := range []string{"tcpmux", "wsmux", "wssmux", "kcp", "xdi", "pck"} {
		if !IsMux(t2) {
			t.Errorf("%s is not reported as a mux transport", t2)
		}
		if !ValidTransport(t2) {
			t.Errorf("%s is a mux transport and not a valid one", t2)
		}
	}
	// Every KCP-carried transport takes the KCP tuning, and each of them is
	// also a datagram transport — the two go together and nothing should be one
	// without the other.
	for _, t2 := range []string{"kcp", "xdi", "pck"} {
		if !IsKCP(t2) || !IsDatagram(t2) {
			t.Errorf("%s: IsKCP=%v IsDatagram=%v, and a KCP carrier is always a datagram one",
				t2, IsKCP(t2), IsDatagram(t2))
		}
	}
	// TLS is terminated by exactly the two secure WebSocket transports, and
	// both of them are WebSocket transports.
	for _, t2 := range []string{"wss", "wssmux"} {
		if !NeedsTLS(t2) || !IsWS(t2) {
			t.Errorf("%s: NeedsTLS=%v IsWS=%v", t2, NeedsTLS(t2), IsWS(t2))
		}
	}
	for _, t2 := range []string{"ws", "wsmux"} {
		if NeedsTLS(t2) {
			t.Errorf("%s is plain WebSocket and was reported as needing TLS", t2)
		}
	}
	// A datagram transport cannot carry the PROXY protocol header on a stream
	// it does not have — except the three that run a stream inside their own
	// carrier, which is why this is a list and not a rule.
	if SupportsProxyProtocol("udp") {
		t.Error("udp was reported as supporting the PROXY protocol")
	}
}

func TestAddressesComeApartTheWayTheyWentTogether(t *testing.T) {
	for _, tc := range []struct{ addr, host, port string }{
		{"127.0.0.1:443", "127.0.0.1", "443"},
		{"[2a01:4f8::1]:443", "2a01:4f8::1", "443"},
		{":443", "fallback", "443"},
		{"443", "fallback", ""},
		{"", "fallback", ""},
	} {
		if got := AddrHost(tc.addr, "fallback"); got != tc.host {
			t.Errorf("AddrHost(%q) = %q, want %q", tc.addr, got, tc.host)
		}
		if got := AddrPort(tc.addr); got != tc.port {
			t.Errorf("AddrPort(%q) = %q, want %q", tc.addr, got, tc.port)
		}
	}
}

// The brackets are the point: an IPv6 bind address arrives written as "[::]",
// and a wildcard that is not recognised as one is a tunnel the panel reports as
// pinned to an address nobody chose.
func TestEveryWayOfSayingEveryInterface(t *testing.T) {
	for _, host := range []string{"", "0.0.0.0", "::", "[::]", "[0.0.0.0]"} {
		if !IsWildcardBind(host) {
			t.Errorf("%q is a wildcard bind and was not recognised as one", host)
		}
	}
	for _, host := range []string{"127.0.0.1", "85.10.11.51", "2a01:4f8::1"} {
		if IsWildcardBind(host) {
			t.Errorf("%q is a specific address and was read as a wildcard", host)
		}
	}
}

func TestAPortSpecIsWhatTheEngineWillAccept(t *testing.T) {
	for _, good := range []string{
		"443", "443=2096", "443=127.0.0.1:2096", "10000-10009", "10000-10009=20000-20009",
		"127.0.0.1:443=2096", "[2a01:4f8::1]:443=127.0.0.1:2053",
	} {
		if !ValidPortSpec(good) {
			t.Errorf("%q is a valid mapping and was refused", good)
		}
	}
	for _, bad := range []string{
		"", "0", "65536", "443=", "443-", "450-443", "abc", "127.0.0.1:443",
	} {
		if ValidPortSpec(bad) {
			t.Errorf("%q is not a valid mapping and was accepted", bad)
		}
	}
}

func TestParsingAListOfPortsDropsTheGaps(t *testing.T) {
	got := ParsePorts(" 443 , , 8080,2053-2060 ,")
	want := []string{"443", "8080", "2053-2060"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
