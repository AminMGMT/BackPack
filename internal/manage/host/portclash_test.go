package host

import (
	"strings"
	"testing"
)

// The report this exists for: one tunnel works, a second will not come up, and
// its log is nothing but EOF.
//
// A server hands out one control channel. Two clients dialling the same server
// and port means the second is refused for as long as it runs — and it is easy
// to do by accident, because copying the tunnel that works and changing the
// name leaves the port pointing at the same place.
func TestASecondClientToTheSameServerIsRefused(t *testing.T) {
	why := clashAgainst("client", "212.23.214.176:3315", "gtest", []tunnel{
		{Name: "germany", Role: "client", Addr: "212.23.214.176:3315"},
	})
	if why == "" {
		t.Fatal("a second client to the same server and port was allowed; it can never " +
			"come up, and the log will only say EOF")
	}
	for _, want := range []string{"germany", "one control channel", "different port"} {
		if !strings.Contains(why, want) {
			t.Errorf("the refusal does not mention %q: %s", want, why)
		}
	}
}

// A different server on the same port number is a different tunnel entirely.
func TestTheSamePortOnADifferentServerIsFine(t *testing.T) {
	if why := clashAgainst("client", "203.0.113.9:3315", "gtest", []tunnel{
		{Name: "germany", Role: "client", Addr: "212.23.214.176:3315"},
	}); why != "" {
		t.Errorf("two clients reaching different servers were treated as a clash: %s", why)
	}
}

// Two listeners cannot share a port: whichever starts second fails to bind.
func TestTwoServersOnOnePortAreRefused(t *testing.T) {
	why := clashAgainst("server", "0.0.0.0:3315", "second", []tunnel{
		{Name: "first", Role: "server", Addr: "0.0.0.0:3315"},
	})
	if why == "" {
		t.Fatal("two servers on one port were allowed")
	}
	if !strings.Contains(why, "first") || !strings.Contains(why, "3315") {
		t.Errorf("the refusal names neither the tunnel nor the port: %s", why)
	}
}

// :: accepts IPv4 too on a dual-stack host — which is why the setup form calls
// it "IPv6 as well" — so it contends with 0.0.0.0 on the same port.
func TestTheWildcardsClashWithEachOther(t *testing.T) {
	if why := clashAgainst("server", "[::]:3315", "second", []tunnel{
		{Name: "first", Role: "server", Addr: "0.0.0.0:3315"},
	}); why == "" {
		t.Error("a :: bind was allowed beside a 0.0.0.0 bind on the same port, which " +
			"is the same socket on a dual-stack host")
	}
}

// A server and a client are different things; a client dialling port 3315
// elsewhere does not stop a server listening on 3315 here.
func TestAServerAndAClientDoNotClash(t *testing.T) {
	if why := clashAgainst("server", "0.0.0.0:3315", "listener", []tunnel{
		{Name: "dialler", Role: "client", Addr: "212.23.214.176:3315"},
	}); why != "" {
		t.Errorf("a server and an unrelated client were treated as a clash: %s", why)
	}
}

// Editing a tunnel must not find the tunnel being edited.
func TestATunnelDoesNotClashWithItself(t *testing.T) {
	if why := clashAgainst("client", "212.23.214.176:3315", "germany", []tunnel{
		{Name: "germany", Role: "client", Addr: "212.23.214.176:3315"},
	}); why != "" {
		t.Errorf("a tunnel clashed with itself: %s", why)
	}
}

// Two servers on one port number are still refused when they would contend for
// it, and allowed when they would not. This is the decision that makes the
// dual-address setup legal, and it already worked — the check compares host and
// port together and treats a wildcard as covering everything. Pinned here so a
// later change to the input path cannot quietly make it unreachable again.
func TestTwoServersMayShareAPortOnDifferentAddresses(t *testing.T) {
	existing := []tunnel{{Name: "control", Role: "server", Addr: "85.10.11.51:443"}}

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
		[]tunnel{{Name: "control", Role: "server", Addr: "0.0.0.0:443"}}); why == "" {
		t.Error("a pinned tunnel alongside a wildcard on the same port was allowed, the other way round")
	}
}
