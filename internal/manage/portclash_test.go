package manage

import (
	"os"
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
	why := clashAgainst("client", "212.23.214.176:3315", "gtest", []Tunnel{
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
	if why := clashAgainst("client", "203.0.113.9:3315", "gtest", []Tunnel{
		{Name: "germany", Role: "client", Addr: "212.23.214.176:3315"},
	}); why != "" {
		t.Errorf("two clients reaching different servers were treated as a clash: %s", why)
	}
}

// Two listeners cannot share a port: whichever starts second fails to bind.
func TestTwoServersOnOnePortAreRefused(t *testing.T) {
	why := clashAgainst("server", "0.0.0.0:3315", "second", []Tunnel{
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
	if why := clashAgainst("server", "[::]:3315", "second", []Tunnel{
		{Name: "first", Role: "server", Addr: "0.0.0.0:3315"},
	}); why == "" {
		t.Error("a :: bind was allowed beside a 0.0.0.0 bind on the same port, which " +
			"is the same socket on a dual-stack host")
	}
}

// A server and a client are different things; a client dialling port 3315
// elsewhere does not stop a server listening on 3315 here.
func TestAServerAndAClientDoNotClash(t *testing.T) {
	if why := clashAgainst("server", "0.0.0.0:3315", "listener", []Tunnel{
		{Name: "dialler", Role: "client", Addr: "212.23.214.176:3315"},
	}); why != "" {
		t.Errorf("a server and an unrelated client were treated as a clash: %s", why)
	}
}

// Editing a tunnel must not find the tunnel being edited.
func TestATunnelDoesNotClashWithItself(t *testing.T) {
	if why := clashAgainst("client", "212.23.214.176:3315", "germany", []Tunnel{
		{Name: "germany", Role: "client", Addr: "212.23.214.176:3315"},
	}); why != "" {
		t.Errorf("a tunnel clashed with itself: %s", why)
	}
}

// Every way of making a tunnel has to refuse the same thing. A check that
// covers the panel and not the wizard covers half the product, which is the
// pattern that put four separate faults in this codebase.
//
// The panel's two entry points build their configuration through specFromNew
// rather than performing the check themselves, so this follows that one step:
// the builder does the checking, and each entry point is required to go through
// the builder. Reaching a tunnel on disk by any other route is the fault this
// guards against, whether the route is a new function or a copy of an old one.
func TestEveryCreationPathChecksForAClash(t *testing.T) {
	if !strings.Contains(manageFuncBody(t, "webapi.go", "specFromNew"), "portClash(") {
		t.Error("specFromNew builds a configuration without checking whether it can " +
			"coexist with the tunnels already there")
	}
	for _, fn := range []string{"CreateTunnel", "ApplyTunnel"} {
		if !strings.Contains(manageFuncBody(t, "webapi.go", fn), "specFromNew(") {
			t.Errorf("%s makes a tunnel without going through specFromNew, so nothing "+
				"checks whether it can coexist with the ones already there", fn)
		}
	}
	if !strings.Contains(manageFuncBody(t, "setup.go", "finishSetup"), "portClash(") {
		t.Error("finishSetup creates a tunnel without checking whether it can coexist " +
			"with the ones already there")
	}
}

// manageFuncBody returns the source of one function in this package.
func manageFuncBody(t *testing.T, file, fn string) string {
	t.Helper()
	src := readManageSource(t, file)
	start := strings.Index(src, "func "+fn+"(")
	if start < 0 {
		t.Fatalf("%s is not in %s any more — this guard needs updating", fn, file)
	}
	body := src[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	return body
}

func readManageSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}
