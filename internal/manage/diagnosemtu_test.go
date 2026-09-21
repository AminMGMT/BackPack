package manage

import (
	"strings"
	"testing"
)

// The MTU check has to reach the transports most likely to be on a hostile
// path.
//
// pathChecks used to return nothing at all for a datagram tunnel — the socket
// table has no entry for one, so the pool and traffic readings genuinely cannot
// be taken. The path MTU can be, and it is the measurement that matters most:
// a tunnel whose path will not carry a full-sized packet comes up, answers
// every check, carries ping and SSH, and stalls every large transfer with
// nothing coming back to say so.
//
// So the transports chosen precisely because the path is interfering were the
// ones told least about it.
func TestDatagramTunnelsAreStillAskedAboutTheirPathMTU(t *testing.T) {
	const host = "203.0.113.7" // TEST-NET-3, reserved for documentation
	checks := datagramPathChecks("Tunnel: t", Tunnel{
		Name: "t", Role: "client", Transport: "kcp",
		Addr: host + ":9000",
	})
	if len(checks) != 1 {
		t.Fatalf("got %d checks, want exactly one — this group used to be skipped "+
			"entirely for datagram transports", len(checks))
	}
	c := checks[0]
	if c.Name != "Path MTU" {
		t.Fatalf("check is %q, want the path MTU", c.Name)
	}

	// Whether the probe reaches anything depends on the network the suite runs
	// on, so the contract is checked rather than the outcome: either it says it
	// could not probe, or it reports a figure and names what it probed.
	switch {
	case strings.Contains(c.Detail, "could not probe"):
		if c.Level != CheckInfo {
			t.Errorf("a failed probe is reported at level %v; it is information, not a fault", c.Level)
		}
	case strings.Contains(c.Detail, host):
		if c.Level != CheckOK && c.Level != CheckWarn {
			t.Errorf("a successful probe is reported at level %v", c.Level)
		}
		// A small path has to come with the number to set, or the operator is
		// told there is a problem and not what to do about it.
		if c.Level == CheckWarn && c.Fix == "" {
			t.Error("a path too small to carry a full packet was reported with no fix")
		}
	default:
		t.Errorf("the check says %q, which neither reports a failure nor names the host "+
			"it measured", c.Detail)
	}
}

// The warning threshold and the figure it suggests have to agree with the
// clamp the rest of the system uses, or the advice is wrong in a way that is
// very hard to notice.
func TestASmallPathSuggestsTheSameMSSTheClampWouldUse(t *testing.T) {
	const mtu = 1280 // a common tunnelled minimum, comfortably under the 1400 warn line
	if got, want := safeMSS(mtu), mtu-20-32; got != want {
		t.Fatalf("safeMSS(%d) = %d, want %d", mtu, got, want)
	}
}

// A server with no connected peer has nowhere to probe, and must say nothing
// rather than probe something wrong.
func TestAServerWithNoPeerIsNotProbed(t *testing.T) {
	if got := peerHostFor(Tunnel{Name: "no-such-tunnel-here", Role: "server"}); got != "" {
		t.Errorf("peerHostFor returned %q for a server with no peer in its snapshot", got)
	}
}

// A client knows where it dials without asking anything.
func TestAClientPeerComesFromItsOwnAddress(t *testing.T) {
	if got := peerHostFor(Tunnel{Role: "client", Addr: "198.51.100.9:443"}); got != "198.51.100.9" {
		t.Errorf("peerHostFor(client) = %q, want 198.51.100.9", got)
	}
}
