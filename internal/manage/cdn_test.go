package manage

import "testing"

// Pointing a tunnel at a CDN-proxied domain is the failure that cost the most
// time to find: everything looks configured, the tunnel appears up, and the
// symptom surfaces much later as an HTTP error page arriving where the protocol
// expected its own bytes. Catching it at setup is worth being precise about.

func TestCDNPortsAreRecognised(t *testing.T) {
	// The ports a CDN will relay. On these, a WebSocket tunnel can work.
	for _, p := range []string{"443", "2053", "2083", "2087", "2096", "8443", "80", "8080"} {
		if !cdnPort(p) {
			t.Errorf("port %s is proxied by CDNs but was not recognised", p)
		}
	}
	// Everything else is not proxied, so a tunnel on it cannot work through one.
	for _, p := range []string{"1231", "1324", "1423", "22", "8989", "7777"} {
		if cdnPort(p) {
			t.Errorf("port %s is not a CDN port but was treated as one", p)
		}
	}
}

// The advice differs completely by transport: a WebSocket tunnel on a proxied
// port can work, and a raw one never can. Getting that backwards would send
// someone down the wrong path for hours.
func TestWebSocketTransportsAreIdentified(t *testing.T) {
	for _, tr := range []string{"ws", "wss", "wsmux", "wssmux"} {
		if !isWS(tr) {
			t.Errorf("%s should be recognised as a websocket transport — it can go through a CDN", tr)
		}
	}
	for _, tr := range []string{"tcp", "tcpmux", "kcp", "udp"} {
		if isWS(tr) {
			t.Errorf("%s is not a websocket transport and cannot go through a CDN", tr)
		}
	}
}

// The exact combination the user hit: WS, but on a port no CDN relays.
func TestWebSocketOnANonCDNPortIsNotViable(t *testing.T) {
	if isWS("ws") && cdnPort("1423") {
		t.Error("ws on 1423 was judged viable through a CDN; that port is not proxied")
	}
}

func TestDetectCDNIgnoresOrdinaryAddresses(t *testing.T) {
	// Documentation addresses belong to nobody.
	if got := detectCDN([]string{"192.0.2.1", "198.51.100.7"}); got != "" {
		t.Errorf("detectCDN reported %q for addresses that belong to no CDN", got)
	}
	if got := detectCDN(nil); got != "" {
		t.Errorf("detectCDN reported %q for no addresses at all", got)
	}
	if got := detectCDN([]string{"not-an-ip"}); got != "" {
		t.Errorf("detectCDN reported %q for a malformed address", got)
	}
}

// Cloudflare is the case that matters here, and the one a reverse lookup gets
// wrong: its addresses carry no PTR record naming it, so a name-based check
// reports "not a CDN" for every one of them.
func TestCloudflareAddressesAreDetected(t *testing.T) {
	// One address from several of the published ranges.
	for _, ip := range []string{
		"104.16.133.229", // 104.16.0.0/13
		"172.66.147.243", // 172.64.0.0/13
		"162.158.1.1",    // 162.158.0.0/15
		"188.114.96.5",   // 188.114.96.0/20
		"131.0.72.9",     // 131.0.72.0/22
	} {
		if got := detectCDN([]string{ip}); got != "Cloudflare" {
			t.Errorf("detectCDN(%s) = %q, want Cloudflare", ip, got)
		}
	}
	// Addresses just outside a range must not be swept up.
	for _, ip := range []string{"104.15.255.255", "131.0.76.1", "8.8.8.8"} {
		if got := detectCDN([]string{ip}); got == "Cloudflare" {
			t.Errorf("detectCDN(%s) wrongly reported Cloudflare", ip)
		}
	}
}

// A domain with both an A and an AAAA record is the trap that makes a name fail
// where the bare address works: resolving yields one address, and it may be the
// IPv6 one, so the tunnel quietly connects over a path that was never tested.
func TestSplitFamilies(t *testing.T) {
	v4, v6 := splitFamilies([]string{
		"46.29.34.18",
		"2a01:4f8:1c17:1234::1",
		"104.16.133.229",
		"::1",
		"not-an-ip",
	})

	if len(v4) != 2 {
		t.Errorf("got %d IPv4 addresses, want 2: %v", len(v4), v4)
	}
	if len(v6) != 2 {
		t.Errorf("got %d IPv6 addresses, want 2: %v", len(v6), v6)
	}
	for _, a := range v4 {
		if a == "not-an-ip" {
			t.Error("a malformed address was classified as IPv4")
		}
	}
}

func TestSplitFamiliesHandlesSingleFamily(t *testing.T) {
	v4, v6 := splitFamilies([]string{"46.29.34.18"})
	if len(v4) != 1 || len(v6) != 0 {
		t.Errorf("IPv4-only domain split into %v / %v", v4, v6)
	}

	v4, v6 = splitFamilies([]string{"2a01:4f8::1"})
	if len(v4) != 0 || len(v6) != 1 {
		t.Errorf("IPv6-only domain split into %v / %v", v4, v6)
	}

	v4, v6 = splitFamilies(nil)
	if len(v4) != 0 || len(v6) != 0 {
		t.Errorf("no addresses split into %v / %v", v4, v6)
	}
}
