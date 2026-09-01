//go:build linux

package manage

import (
	"os"
	"testing"

	"github.com/backpack/backpack/internal/utils/network"
)

// A gated live check of the relaxation: it flips a strict rp_filter to loose and
// the reading agrees afterwards. It needs a writable /proc/sys and a raw-ish
// host, so it runs only under BP_RPFILTER_LIVE (the netns rig sets it) and skips
// in the ordinary suite.
func TestRelaxRPFilterLive(t *testing.T) {
	if os.Getenv("BP_RPFILTER_LIVE") == "" {
		t.Skip("set BP_RPFILTER_LIVE=1 in a namespace with a writable /proc to run this")
	}
	// Precondition: something made conf.all strict.
	if v, _ := network.EffectiveRPFilter(""); v != 1 {
		t.Fatalf("expected a strict rp_filter to start from, got %d", v)
	}
	if err := relaxRPFilter([]string{"net.ipv4.conf.all.rp_filter"}); err != nil {
		t.Fatalf("relaxRPFilter: %v", err)
	}
	if v, key := network.EffectiveRPFilter(""); v == 1 {
		t.Fatalf("%s is still strict after relaxing", key)
	}
	// Persistence is best-effort — a host where /etc/sysctl.d cannot be written
	// (a mapped-root namespace, a read-only /etc) still gets the live change,
	// which is what this asserts. On a real root server the file is written too.
	if _, err := os.Stat(rpFilterSysctlFile); err == nil {
		t.Logf("relaxation also persisted to %s", rpFilterSysctlFile)
	} else {
		t.Logf("live change applied; persistence skipped (%v) — expected off a real root fs", err)
	}
}
