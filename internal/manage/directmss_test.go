package manage

import (
	"strings"
	"testing"
)

// The direct tunnel was the only one of the three kinds with no way to cap its
// TCP segment size.
//
// The reverse tunnel has had one since the failure was diagnosed there, and
// [l3] measures the path itself. Direct crosses the same Iran-to-abroad paths
// and had neither: where a path carries less than a full-sized packet and drops
// the oversized ones without an ICMP reply, the handshake and the mux's
// keepalives are small enough to arrive, so the tunnel comes up and looks
// healthy while every real transfer stalls on the first full segment. The
// socket stays ESTABLISHED throughout, so the watchdog sees nothing wrong
// either, and the operator is left restarting it by hand.
func TestADirectTunnelCanCapItsSegmentSize(t *testing.T) {
	spec := directSpec{
		Name: "iran-main", Side: sideIran, Transport: "tcp",
		Addr: "203.0.113.10:8443", Token: "a-long-token",
		Ports: []string{"443"},
		MSS:   1360,
	}

	out := spec.render()
	if !strings.Contains(out, "mss") {
		t.Fatal("the rendered [direct] config carries no mss key, so the cap cannot " +
			"reach the engine")
	}
	if !strings.Contains(out, "1360") {
		t.Errorf("the cap was not written out:\n%s", out)
	}
}

// Off unless asked for. A key written as a zero would claim a setting the
// tunnel does not have, and the kernel's own choice is right almost always.
func TestTheSegmentCapIsAbsentWhenUnset(t *testing.T) {
	spec := directSpec{
		Name: "iran-main", Side: sideIran, Transport: "tcp",
		Addr: "203.0.113.10:8443", Token: "a-long-token",
		Ports: []string{"443"},
	}

	for _, line := range strings.Split(spec.render(), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "mss") {
			t.Errorf("an unset cap was written out as %q", trimmed)
		}
	}
}

// An edit re-renders the whole file, so a key the spec cannot hold is a key the
// edit silently deletes. That is the trap the spec's own comment warns about,
// and a cap set by hand must survive a port change made from the menu.
func TestAnEditKeepsAHandSetSegmentCap(t *testing.T) {
	spec := directSpec{
		Name: "iran-main", Side: sideIran, Transport: "tcp",
		Addr: "203.0.113.10:8443", Token: "a-long-token",
		Ports: []string{"443", "8443"},
		MSS:   1208,
	}

	if !strings.Contains(spec.render(), "1208") {
		t.Fatal("re-rendering after an edit dropped the segment cap")
	}
}

// The label is what the menu shows, and a bare zero there reads as a cap of
// zero rather than as no cap at all.
func TestTheSegmentCapLabelExplainsAZero(t *testing.T) {
	if got := directMSSLabel(0); !strings.Contains(got, "kernel") {
		t.Errorf("an unset cap is shown as %q, which does not say who decides", got)
	}
	if got := directMSSLabel(1360); !strings.Contains(got, "1360") {
		t.Errorf("a set cap is shown as %q", got)
	}
}
