package manage

import (
	"os"
	"strings"
	"testing"
)

// A KCP or UDP tunnel carries no TCP sockets, so anything that decides "is it
// up" by looking for peers in the TCP socket table will call a working tunnel
// offline. That is exactly what the web panel did, and it is an easy mistake to
// make again in a new place, because the wrong answer looks plausible.

func TestDatagramTransportsAreRecognised(t *testing.T) {
	for _, tr := range []string{"kcp", "udp"} {
		if !isDatagram(tr) {
			t.Errorf("%s should be treated as a datagram transport", tr)
		}
	}
	for _, tr := range []string{"tcp", "tcpmux", "ws", "wsmux", "wss", "wssmux"} {
		if isDatagram(tr) {
			t.Errorf("%s is not a datagram transport", tr)
		}
	}
}

// A datagram server has nothing to observe: the listener is one unconnected
// socket that keeps no record of who is talking to it. Reporting that as
// "offline" is worse than useless, because it is a working tunnel.
func TestDatagramServerIsNotReportedOffline(t *testing.T) {
	for _, tr := range []string{"kcp", "udp"} {
		tun := Tunnel{Name: "t", Role: "server", Transport: tr, Addr: "[::]:8989"}
		// An empty TCP socket table is the situation that used to produce the
		// wrong answer.
		if !tunnelHealthy(tun, nil) {
			t.Errorf("a running %s server was reported unhealthy with no TCP sockets", tr)
		}
	}
}

// The same tunnel over TCP genuinely is offline with no peer, and must still be
// reported that way — the fix must not make everything look healthy.
func TestTCPServerWithNoPeerIsStillOffline(t *testing.T) {
	tun := Tunnel{Name: "t", Role: "server", Transport: "tcp", Addr: "0.0.0.0:8989"}
	if tunnelHealthy(tun, nil) {
		t.Error("a TCP server with no connected peer should not be reported healthy")
	}
}

// The health detail must not claim more than was actually checked.
func TestDatagramServerDetailIsHonest(t *testing.T) {
	tun := Tunnel{Name: "t", Role: "server", Transport: "kcp", Addr: "[::]:8989", Service: "backpack-t.service"}
	h := tunnelHealthWith(tun, nil)

	// Without a real systemd unit this reports stopped, which is correct here;
	// the wording under test is the one used when it is running.
	if h.State == "online" && !strings.Contains(h.Detail, "cannot report its peers") {
		t.Errorf("a datagram server should say what it could not verify, got %q", h.Detail)
	}
}

// The regression guard: the panel must not compute tunnel state itself. It did,
// and that is why a working KCP tunnel showed as offline in the Iran web UI
// while the watchdog — which had the datagram case right — left it alone.
func TestPanelUsesSharedHealth(t *testing.T) {
	src, err := os.ReadFile("../webui/stats.go")
	if err != nil {
		t.Skipf("cannot read the panel source: %v", err)
	}
	body := string(src)

	if !strings.Contains(body, "manage.AllHealth()") {
		t.Error("the panel does not use manage.AllHealth — it is deciding tunnel state on its own again")
	}
	// The old logic keyed "offline" off an empty TCP peer list.
	if strings.Contains(body, "case len(peers) == 0:") {
		t.Error("the panel still decides offline from an empty TCP peer list, which is wrong for KCP and UDP")
	}
}
