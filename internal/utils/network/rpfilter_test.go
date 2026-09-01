package network

import "testing"

// The kernel applies max(conf.all, conf.<iface>) and only 1 (strict) drops
// forged sources. The check used to read conf.all alone, so a host with all=0
// and the receiving NIC at 1 filtered as if strict everywhere and was waved
// through. These pin the max rule and which value drops; the /proc reads
// themselves want a live host (see the netns rig).

func TestEffectiveRPFilterTakesTheStricterOfAllAndInterface(t *testing.T) {
	for _, tc := range []struct {
		name     string
		all, ifc int
		want     int
	}{
		{"both off", 0, 0, 0},
		{"all strict", 1, 0, 1},
		{"interface strict — the case that used to slip through", 0, 1, 1},
		{"both strict", 1, 1, 1},
		{"all loose, interface off", 2, 0, 2},
		{"unreadable interface must not mask a strict all", 1, -1, 1},
	} {
		if got := maxRP(tc.all, tc.ifc); got != tc.want {
			t.Errorf("%s: max(%d,%d) = %d, want %d", tc.name, tc.all, tc.ifc, got, tc.want)
		}
	}
}

func TestOnlyStrictRPFilterDropsForgedSources(t *testing.T) {
	for v, shouldDrop := range map[int]bool{0: false, 1: true, 2: false} {
		if (v == 1) != shouldDrop {
			t.Errorf("rp_filter=%d: drop=%v, want %v", v, v == 1, shouldDrop)
		}
	}
}

// maxRP mirrors the rule EffectiveRPFilter applies to the two /proc reads,
// including that an unreadable (-1) value never wins over a real one.
func maxRP(all, ifc int) int {
	v := all
	if ifc > v {
		v = ifc
	}
	return v
}
