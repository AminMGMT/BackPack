package manage

import (
	"os"
	"strings"
	"testing"
)

// The Edit screen for the spoof carrier is a summary and a menu, and the
// summary is what somebody reads before deciding whether to change anything.
// These pin the readings that would otherwise be wrong in a way nobody catches:
// a pool reported as one address, or "nothing on" over a tunnel that is padding
// every frame.

func TestSpoofSummariesReadTheTunnelBack(t *testing.T) {
	cases := []struct {
		name string
		spec TunnelSpec
		want map[string]string // summary function name → substring it must hold
	}{
		{
			name: "a plain tunnel says so rather than showing blanks",
			spec: TunnelSpec{Transport: "spoof", SpoofProfile: "udp"},
			want: map[string]string{
				"profile":  "udp",
				"source":   "real address",
				"pipe":     "off",
				"evasion":  "nothing on",
				"iface":    "automatic",
				"peerNone": "none",
			},
		},
		{
			name: "a pool is reported as a pool, not as its first address",
			spec: TunnelSpec{
				Transport: "spoof", SpoofProfile: "udp",
				SpoofSrcIP:   "203.0.113.10",
				SpoofSrcPool: []string{"203.0.113.10", "198.51.100.7"},
			},
			want: map[string]string{"source": "2 addresses"},
		},
		{
			name: "an asymmetric tunnel names both directions",
			spec: TunnelSpec{
				Transport: "spoof", SpoofProfile: "udp",
				SpoofUplink: "icmp",
			},
			want: map[string]string{"profile": "icmp up, udp down"},
		},
		{
			name: "what is turned on is named",
			spec: TunnelSpec{
				Transport: "spoof", SpoofProfile: "tcp",
				SpoofPadding: true, SpoofTTLJitter: true, SpoofMTU: 1200,
				SpoofPipe: true, SpoofPipeAddr: "127.0.0.1:51821",
			},
			want: map[string]string{
				"evasion": "padding",
				"pipe":    "127.0.0.1:51821",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := map[string]string{
				"profile":  spoofProfileSummary(c.spec),
				"source":   spoofSourceSummary(c.spec),
				"pipe":     spoofPipeSummary(c.spec),
				"evasion":  spoofEvasionSummary(c.spec),
				"iface":    orAuto(c.spec.SpoofInterface),
				"peerNone": orNone(c.spec.SpoofPeerIP),
			}
			for k, want := range c.want {
				if !strings.Contains(got[k], want) {
					t.Errorf("%s summary reads %q, wanted it to mention %q", k, got[k], want)
				}
			}
		})
	}
}

// The evasion summary lists what is on, so a tunnel with several knobs set says
// so rather than naming only the first.
func TestEvasionSummaryNamesEverythingThatIsOn(t *testing.T) {
	s := TunnelSpec{
		Transport: "spoof",
		SpoofTTLJitter: true, SpoofRandomDSCP: true, SpoofShufflePort: true,
		SpoofPadding: true, SpoofFakeTLS: true, SpoofICMPReply: true, SpoofMTU: 1300,
	}
	got := spoofEvasionSummary(s)
	for _, want := range []string{"TTL jitter", "DSCP", "port shuffle", "padding", "fake TLS", "ICMP reply", "fragmenting"} {
		if !strings.Contains(got, want) {
			t.Errorf("the summary does not mention %q: %q", want, got)
		}
	}
}

// SetSpoof is the terminal's way into the same block the panel posts, so it has
// to refuse the same things — and refuse a tunnel that is not on the carrier at
// all, which is the one mistake the menu itself cannot make.
func TestSetSpoofRefusesTheWrongTransport(t *testing.T) {
	// LoadSpec reads from disk, so the check that does not need a file is the
	// one worth pinning here; the rest of the validation is covered against
	// SpoofTune.apply directly.
	s := TunnelSpec{Role: "server", Transport: "tcp"}
	if err := (SpoofTune{Profile: "udp", PeerIP: "203.0.113.10"}).apply(&s); err != nil {
		t.Fatalf("apply should be a no-op off the spoof transport: %v", err)
	}
	if s.SpoofProfile != "" || s.SpoofPeerIP != "" {
		t.Errorf("spoof settings were written onto a tcp tunnel: %+v", s)
	}
}

// The Edit menu builds its entries and its handlers side by side, so an entry
// that only exists for some transports cannot end up running the action below
// it. The spoof entry has to be on both halves of that menu — the carrier runs
// on both ends, and the Iran side is the one with a setting the tunnel cannot
// work without.
func TestEditMenuOffersSpoofOnBothRoles(t *testing.T) {
	src, err := os.ReadFile("menu.go")
	if err != nil {
		t.Fatalf("cannot read the menu: %v", err)
	}
	for _, want := range []string{
		`Title: "IP Spoofing"`,
		`actions = append(actions, func() { editSpoof(name, spec) })`,
	} {
		if n := strings.Count(string(src), want); n != 2 {
			t.Errorf("expected %q once per role, found %d", want, n)
		}
	}
}
