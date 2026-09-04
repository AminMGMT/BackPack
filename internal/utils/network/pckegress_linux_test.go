package network

import (
	"os"
	"strings"
	"testing"
)

// The report this exists for: "pck direct tunnels cannot be set up."
//
// The listening end refused to start, saying:
//
//	pck: no route to 1.1.1.1
//
// which names an address the operator never configured and nothing is ever
// sent to. It is a stand-in: pck writes raw frames, so it has to know which
// interface to send from, and the client resolves that by routing toward its
// peer. A server has no single peer, so it routed toward 1.1.1.1 to find the
// default route — and a machine with no default route in the main table could
// not run a pck listener at all. Policy routing in another table, an IPv6-only
// default, a private segment, a namespace: all of them.
//
// A server's replies go back to whoever reached it, so any interface that can
// carry them will do. Guessing wrongly is recoverable with pck_interface;
// refusing to start is not.

func TestAListenerHasAnInterfaceToFallBackOn(t *testing.T) {
	name := firstUsableIface()
	if name == "" {
		t.Skip("this machine has no interface up with an IPv4 address")
	}
	if strings.EqualFold(name, "lo") {
		t.Error("the fallback picked loopback, which cannot carry a tunnel")
	}
}

// And the fallback is only the server's. A client routing toward a real peer
// that has no route is a fault worth stopping for: sending its frames out of
// an arbitrary interface would be worse than saying so.
func TestOnlyTheListenerFallsBack(t *testing.T) {
	src, err := os.ReadFile("pckconn_linux.go")
	if err != nil {
		t.Fatalf("cannot read pckconn_linux.go: %v", err)
	}
	body := string(src)

	i := strings.Index(body, "func newPckConn(")
	if i < 0 {
		t.Fatal("newPckConn is gone — this guard needs updating")
	}
	fn := body[i:]
	if end := strings.Index(fn, "\n}\n"); end > 0 {
		fn = fn[:end]
	}
	if !strings.Contains(fn, "firstUsableIface()") {
		t.Error("a pck listener no longer falls back when the route lookup finds " +
			"nothing, so a machine with no default route cannot run one")
	}
	// The fallback has to be conditional on being the server, and on the
	// operator not having named an interface themselves — or it would also
	// cover the client, whose destination is a real peer and for whom no route
	// is a real error.
	if !strings.Contains(fn, `if err != nil && server && carrier.Interface == ""`) {
		t.Error("the fallback is not guarded on being the listener with no interface " +
			"of its own; a client with no route to its peer would now send its " +
			"frames out of whichever interface happened to be first")
	}
}
