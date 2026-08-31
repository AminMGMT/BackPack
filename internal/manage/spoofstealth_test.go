package manage

import (
	"reflect"
	"strings"
	"testing"

	"github.com/backpack/backpack/config"
)

// Stealth is one answer standing for several settings, and the reason it is one
// answer is that two of them change the wire and so must match at the far end.
// These pin the parts of that where being close is the same as being wrong.

// On and off again must land exactly where it started, or a tunnel switched off
// keeps a port range and a padding ceiling nothing reads — and the Edit screen
// goes on describing a tunnel that is not running.
func TestStealthOffUndoesStealthOnExactly(t *testing.T) {
	before := config.SpoofConfig{SpoofProfile: "udp", SpoofSrcIP: "203.0.113.10"}

	sc := before
	applySpoofStealth(&sc)
	if reflect.DeepEqual(sc, before) {
		t.Fatal("turning Stealth on changed nothing")
	}
	clearSpoofStealth(&sc)
	if !reflect.DeepEqual(sc, before) {
		t.Errorf("off did not undo on:\n got %+v\nwant %+v", sc, before)
	}
}

// The whole group has to arrive, not most of it. A half-applied Stealth is the
// worst outcome available: it costs the throughput and leaves the fingerprint.
func TestStealthTurnsOnEveryKnobItPromises(t *testing.T) {
	sc := config.SpoofConfig{SpoofProfile: "udp"}
	applySpoofStealth(&sc)

	if !sc.SpoofPadding || sc.SpoofPaddingMax <= 0 {
		t.Error("padding is not on, or has no ceiling to pad up to")
	}
	if !sc.SpoofTTLJitter || !sc.SpoofRandomDSCP {
		t.Error("the header cosmetics are not on")
	}
	if !sc.SpoofShufflePort || sc.SpoofPortMin <= 0 || sc.SpoofPortMax <= sc.SpoofPortMin {
		t.Errorf("the source port shuffle has no usable range: %d-%d", sc.SpoofPortMin, sc.SpoofPortMax)
	}
}

// The fake TLS record header is a tcp-profile thing. Setting it anywhere else
// would put a key in the config that the carrier reads and ignores, which is
// how a setting comes to be believed in without ever having done anything.
func TestFakeTLSFollowsTheProfile(t *testing.T) {
	for _, tc := range []struct {
		name              string
		profile, up, down string
		want              bool
	}{
		{"udp", "udp", "", "", false},
		{"icmp", "icmp", "", "", false},
		{"tcp", "tcp", "", "", true},
		{"tcp uplink only", "udp", "tcp", "", true},
		{"tcp downlink only", "udp", "", "tcp", true},
	} {
		sc := config.SpoofConfig{SpoofProfile: tc.profile, SpoofUplink: tc.up, SpoofDownlink: tc.down}
		applySpoofStealth(&sc)
		if sc.SpoofFakeTLS != tc.want {
			t.Errorf("%s: fake TLS = %v, want %v", tc.name, sc.SpoofFakeTLS, tc.want)
		}
	}
}

// What the summary and the menu call "Stealth on" has to mean the paired half —
// the settings the other end must match. The local-only cosmetics are nobody
// else's business, and reporting them as Stealth would tell an operator to go
// and change a setting on the far machine that does not need changing.
func TestStealthIsReportedOnTheSettingsThatHaveToMatch(t *testing.T) {
	local := config.SpoofConfig{SpoofTTLJitter: true, SpoofRandomDSCP: true, SpoofShufflePort: true}
	if spoofStealthOn(local) {
		t.Error("local-only cosmetics were reported as Stealth")
	}
	for _, sc := range []config.SpoofConfig{{SpoofPadding: true}, {SpoofFakeTLS: true}} {
		if !spoofStealthOn(sc) {
			t.Errorf("a wire-changing setting was not reported as Stealth: %+v", sc)
		}
	}
}

// The carrier's entry sits at the end of the edit menu on both sides, which is
// the only reason it needs no arithmetic of its own: the two Iran-only entries
// go above it, and l3EditAction's shift already accounts for them.
//
// This is the assumption the entry was added on. If the menu ever grows an
// entry after it, this fails rather than silently editing the MTU when somebody
// asked for the forged source.
func TestTheSpoofEntryIsTheLastActionFromEitherSide(t *testing.T) {
	const spoofAction = 5

	// Iran sees ports, UDP, MTU, segment cap, token, spoof.
	if got, ok := l3EditAction(5, true); !ok || got != spoofAction {
		t.Errorf("Iran's last entry = action %d (ok=%v), want %d", got, ok, spoofAction)
	}
	// Kharej sees MTU, segment cap, token, spoof.
	if got, ok := l3EditAction(3, false); !ok || got != spoofAction {
		t.Errorf("kharej's last entry = action %d (ok=%v), want %d", got, ok, spoofAction)
	}
	// And going back is still going back from either.
	for _, iran := range []bool{true, false} {
		if _, ok := l3EditAction(-1, iran); ok {
			t.Errorf("iran=%v: going back was read as an action", iran)
		}
	}
}

// The menu line has to say the two things that must agree with the other
// machine, because that is what an operator is comparing when they read it.
func TestTheCarrierSummarySaysTheProfileAndStealth(t *testing.T) {
	sc := config.SpoofConfig{SpoofProfile: "icmp", SpoofSrcPool: []string{"1.1.1.1", "8.8.8.8"}}
	applySpoofStealth(&sc)

	got := spoofCarrierSummary(sc)
	for _, want := range []string{"icmp", "2 forged sources", "Stealth on"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q does not say %q", got, want)
		}
	}

	plain := spoofCarrierSummary(config.SpoofConfig{})
	for _, want := range []string{"udp", "unforged", "Stealth off"} {
		if !strings.Contains(plain, want) {
			t.Errorf("an unset carrier summarised as %q, which does not say %q", plain, want)
		}
	}
}
