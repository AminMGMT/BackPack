package health

import (
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
		// that produces a suggestion at all. The threshold is oversizeMSS, not
		// safeMSS: the kernel's snd_mss has the option bytes out of it already,
		// so a segment is only genuinely too large once it passes mtu-40. The
		// suggestion is still safeMSS, which is what this asserts is settable.
		c := pathMTUCheck("g", mtu, oversizeMSS(mtu)+1, true)
		if c.Level != CheckFail {
			t.Fatalf("mtu %d: oversized segments were not reported as a fault (%v)", mtu, c.Level)
		}
		clamp := safeMSS(mtu)
		if clamp < MinMSS {
			// Below the IPv4 minimum the honest answer is that no clamp helps,
			// so no number may be offered.
			if strings.Contains(c.Fix, "MSS clamp") {
				t.Fatalf("mtu %d: suggested a clamp of %d, which is below the %d floor SetMSS enforces",
					mtu, clamp, MinMSS)
			}
			continue
		}
		if clamp > MaxMSS {
			t.Fatalf("mtu %d: suggested a clamp of %d, above the %d ceiling SetMSS enforces",
				mtu, clamp, MaxMSS)
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
		if c := pathMTUCheck("g", tc.mtu, tc.negotiated, true); c.Level != CheckOK {
			t.Fatalf("mtu %d with mss %d reported as %v: %s", tc.mtu, tc.negotiated, c.Level, c.Detail)
		}
	}
}

// A connection the kernel has not reported an mss for yet must not be read as
// "sending zero-byte segments" and turned into a fault.
func TestUnknownSegmentSizeIsNotAFault(t *testing.T) {
	if c := pathMTUCheck("g", 1260, 0, true); c.Level == CheckFail {
		t.Fatalf("an unmeasured connection was reported as a fault: %s", c.Detail)
	}
}
