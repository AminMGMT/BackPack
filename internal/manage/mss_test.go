package manage

import (
	"fmt"
	"strings"
	"testing"
)

// The MSS clamp exists because one fault in this system is invisible from every
// other angle: a path that cannot carry a full-sized packet and drops the
// oversized ones without an ICMP reply. The tunnel connects, stays up, and
// stalls on every real transfer. Diagnose measures it and names the number;
// SetMSS is where that number goes.
//
// What these tests protect is the join between those two. A check that prints a
// fix the tunnel then refuses is worse than no check at all — it sends the
// operator looking for a setting that will not take the value they were told to
// use, which is exactly the dead end this feature was built to remove.

// Every clamp pathMTUCheck can print must be one SetMSS accepts, across the
// whole range of path MTUs the probe can report.
func TestSuggestedClampIsAlwaysAccepted(t *testing.T) {
	// probePathMTU returns its binary search result plus 28 bytes of headers.
	// Its smallest possible answer is the initial best of 64, its largest the
	// top of the search range.
	for mtu := 64 + 28; mtu <= 1472+28; mtu++ {
		// The tunnel is sending more than the path carries — the only case
		// that produces a suggestion at all.
		c := pathMTUCheck("g", mtu, safeMSS(mtu)+1)
		if c.Level != CheckFail {
			t.Fatalf("mtu %d: oversized segments were not reported as a fault (%v)", mtu, c.Level)
		}
		clamp := safeMSS(mtu)
		if clamp < minMSS {
			// Below the IPv4 minimum the honest answer is that no clamp helps,
			// so no number may be offered.
			if strings.Contains(c.Fix, "MSS clamp") {
				t.Fatalf("mtu %d: suggested a clamp of %d, which is below the %d floor SetMSS enforces",
					mtu, clamp, minMSS)
			}
			continue
		}
		if clamp > maxMSS {
			t.Fatalf("mtu %d: suggested a clamp of %d, above the %d ceiling SetMSS enforces",
				mtu, clamp, maxMSS)
		}
	}
}

// A path that comfortably carries full-sized packets must not be reported as a
// fault, or the check cries wolf on every healthy tunnel.
func TestHealthyPathIsNotAFault(t *testing.T) {
	for _, tc := range []struct{ mtu, negotiated int }{
		{1500, 1448}, // the ordinary case
		{1500, 1400}, // already inside the path
		{1400, 1348}, // a smaller path the tunnel has adapted to
	} {
		if c := pathMTUCheck("g", tc.mtu, tc.negotiated); c.Level != CheckOK {
			t.Fatalf("mtu %d with mss %d reported as %v: %s", tc.mtu, tc.negotiated, c.Level, c.Detail)
		}
	}
}

// A connection the kernel has not reported an mss for yet must not be read as
// "sending zero-byte segments" and turned into a fault.
func TestUnknownSegmentSizeIsNotAFault(t *testing.T) {
	if c := pathMTUCheck("g", 1260, 0); c.Level == CheckFail {
		t.Fatalf("an unmeasured connection was reported as a fault: %s", c.Detail)
	}
}

// The clamp describes the path, not the performance profile, so a preset change
// must leave it alone. If it did not, changing preset would silently undo the
// fix and hand back the stalled tunnel it was applied to cure.
func TestPresetDoesNotTouchTheClamp(t *testing.T) {
	for _, p := range []string{PresetBalance, PresetTurbo, PresetAggressive} {
		s := TunnelSpec{MSS: 1208}
		ApplyPreset(&s, p)
		if s.MSS != 1208 {
			t.Fatalf("%s preset changed the MSS clamp to %d", p, s.MSS)
		}
	}
}

// The panel's Fine Tune drawer reads the clamp off the tunnel and writes it
// back. A zero has to survive that round trip as a zero: it is the answer
// "let the kernel choose", and clearing the box is how the clamp is removed.
func TestFineTuneCarriesTheClampBothWays(t *testing.T) {
	s := TunnelSpec{Role: "server", Transport: "wss", MSS: 1208}
	if got := tuneOf(s).MSS; got != 1208 {
		t.Fatalf("the drawer would open on mss %d, not the 1208 the tunnel runs", got)
	}

	tune := tuneOf(s)
	tune.apply(&s)
	if s.MSS != 1208 {
		t.Fatalf("an untouched drawer changed the clamp to %d", s.MSS)
	}

	tune.MSS = 0
	tune.apply(&s)
	if s.MSS != 0 {
		t.Fatalf("clearing the clamp left it at %d — there would be no way to undo one", s.MSS)
	}
}

// The clamp only reaches the config file when it is set, so a tunnel that never
// had one does not grow an `mss = 0` line.
func TestClampIsWrittenOnlyWhenSet(t *testing.T) {
	render := func(s TunnelSpec) string {
		var b strings.Builder
		s.writeTuning(func(f string, a ...any) { b.WriteString(fmt.Sprintf(f, a...)) })
		return b.String()
	}
	if got := render(TunnelSpec{MSS: 1208}); !strings.Contains(got, "mss = 1208") {
		t.Fatalf("a set clamp was not written to the config: %q", got)
	}
	if got := render(TunnelSpec{}); strings.Contains(got, "mss") {
		t.Fatalf("an unset clamp was written anyway: %q", got)
	}
}
