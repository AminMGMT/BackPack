package transport

import (
	"os"
	"strings"
	"testing"
)

// A connection slot taken on accept has to be given back on every path out.
//
// Each of these transports reserves a slot the moment it accepts a forwarded
// connection — deliberately, so a refused connection is refused before it costs
// anything — and the handler goroutine gives it back when the transfer ends.
// The pairing timeout is the path where that goroutine never runs: the client
// waited three seconds for a tunnel connection, none arrived, and the
// connection is closed without ever being handed to a handler.
//
// tcp and quic freed the slot there. tcpmux, wsmux, kcp and ws did not, so a
// tunnel with max_connections set lost a slot to every timeout and eventually
// refused everything, with a limit that looked correct in the config and a
// panel that showed no connections at all. The shape is identical in all six
// and the bug was in four of them, which is the argument for checking them
// together rather than one at a time.
func TestEveryPairingTimeoutFreesItsConnectionSlot(t *testing.T) {
	// udp is absent on purpose: it never acquires a slot, because
	// max_connections is not wired to it at all. That is its own defect and it
	// is not this one — adding udp here would make this test fail for a reason
	// it does not describe.
	for _, name := range []string{"tcp", "tcpmux", "ws", "wsmux", "kcp", "quic"} {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(name + ".go")
			if err != nil {
				t.Fatalf("%s.go: %v", name, err)
			}
			branch, ok := timeoutBranch(string(src))
			if !ok {
				t.Fatalf("%s.go has no pairing-timeout branch — either it was removed "+
					"or the log line this test finds it by was reworded", name)
			}
			if !strings.Contains(branch, "s.limits.release()") {
				t.Errorf("%s.go closes a connection that timed out waiting to be paired "+
					"without freeing the slot it took on accept:\n%s", name, branch)
			}
		})
	}
}

// Every one of these transports must still take a slot, or the release above is
// balancing nothing.
func TestEveryTransportStillTakesASlotOnAccept(t *testing.T) {
	for _, name := range []string{"tcp", "tcpmux", "ws", "wsmux", "kcp", "quic"} {
		src, err := os.ReadFile(name + ".go")
		if err != nil {
			t.Fatalf("%s.go: %v", name, err)
		}
		if !strings.Contains(string(src), "s.limits.acquire()") {
			t.Errorf("%s.go no longer enforces the connection limit on accept", name)
		}
	}
}

// timeoutBranch returns the body of the pairing-timeout branch: from the line
// that logs the timeout to whatever leaves the branch.
//
// Anchored on the log line because it is the one thing all six write identically
// and it sits at the top of the branch in every one of them.
func timeoutBranch(src string) (string, bool) {
	const anchor = `timeouted local connection`
	i := strings.Index(src, anchor)
	if i < 0 {
		return "", false
	}
	rest := src[i:]
	end := len(rest)
	for _, exit := range []string{"break loop", "continue", "return"} {
		if j := strings.Index(rest, exit); j >= 0 && j < end {
			end = j + len(exit)
		}
	}
	return rest[:end], true
}
