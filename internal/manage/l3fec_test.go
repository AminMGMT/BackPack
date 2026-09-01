package manage

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tunnel/l3"
)

// Error correction is a paired setting written as two keys, and the way it gets
// lost is not the wizard — it is an edit. An edit re-renders the whole file from
// a spec, so a key the spec cannot hold vanishes the moment somebody changes the
// MTU: the tunnel comes back up without the correction it was set up with, on
// the same lossy path, and nothing says so.

func TestFECSurvivesARenderRoundTrip(t *testing.T) {
	spec := l3Spec{
		Name: "lossy", Side: sideIran, Carrier: "udp",
		Addr: "203.0.113.9:9000", Token: "a-token-0123456789abcdefghijklmno",
		LocalIP: "10.20.0.1/30", PeerIP: "10.20.0.2",
		Ports:     []string{"443"},
		FECData:   10,
		FECParity: 3,
	}
	var got config.Config
	if _, err := toml.Decode(spec.render(), &got); err != nil {
		t.Fatalf("the rendered config does not parse: %v", err)
	}
	if got.L3.FECData != 10 || got.L3.FECParity != 3 {
		t.Errorf("fec did not survive: data=%d parity=%d", got.L3.FECData, got.L3.FECParity)
	}
}

// Half a scheme is not a scheme — the engine refuses it — so the renderer must
// never write one key without the other.
func TestAHalfSchemeIsNotRendered(t *testing.T) {
	for _, spec := range []l3Spec{
		{Name: "a", Side: sideIran, Carrier: "udp", Addr: "203.0.113.9:9000",
			Token: "a-token-0123456789abcdefghijklmno", LocalIP: "10.20.0.1/30",
			PeerIP: "10.20.0.2", Ports: []string{"443"}, FECData: 10},
		{Name: "b", Side: sideIran, Carrier: "udp", Addr: "203.0.113.9:9000",
			Token: "a-token-0123456789abcdefghijklmno", LocalIP: "10.20.0.1/30",
			PeerIP: "10.20.0.2", Ports: []string{"443"}, FECParity: 3},
	} {
		if out := spec.render(); strings.Contains(out, "fec_") {
			t.Errorf("a half-configured scheme was written out:\n%s", out)
		}
	}
}

// And a tunnel with no correction writes no keys at all, so a clean-path config
// does not carry settings it never uses.
func TestNoFECWritesNothing(t *testing.T) {
	spec := l3Spec{
		Name: "clean", Side: sideIran, Carrier: "udp",
		Addr: "203.0.113.9:9000", Token: "a-token-0123456789abcdefghijklmno",
		LocalIP: "10.20.0.1/30", PeerIP: "10.20.0.2", Ports: []string{"443"},
	}
	if out := spec.render(); strings.Contains(out, "fec_") {
		t.Errorf("a tunnel with no error correction rendered fec keys:\n%s", out)
	}
}

// The recommended pair has to be one the engine accepts, or the one-question
// answer produces a tunnel that refuses to start. It comes from RecommendFEC,
// so this also pins that the two features agree on what an unmeasured path
// deserves rather than each carrying its own number.
func TestTheRecommendedSchemeIsValid(t *testing.T) {
	plan := defaultL3FEC()
	if !plan.Set() {
		t.Fatal("the recommended scheme is not a scheme")
	}
	if plan.Parity >= plan.Data {
		t.Fatalf("the recommended scheme sends %d spare per %d — more redundancy than payload",
			plan.Parity, plan.Data)
	}
	// And the engine must accept it, which is the check that actually matters.
	if err := (l3FECFrom(plan)).Validate(); err != nil {
		t.Errorf("the engine refuses the wizard's recommendation: %v", err)
	}
}

// l3FECFrom turns a plan into the engine's scheme, so the test asserts against
// the type the engine actually validates rather than a copy of its rules.
func l3FECFrom(p FECPlan) l3.FECConfig {
	return l3.FECConfig{Data: p.Data, Parity: p.Parity}
}

// Spreading over several sockets is written and read back the same way error
// correction is, and for the same reason: an edit re-renders the file, and a
// key the spec cannot hold is a setting that disappears the next time somebody
// changes the MTU — here leaving a tunnel back on one socket and one speed
// limit, with nothing to say why it got slower.
func TestPathsSurviveARenderRoundTrip(t *testing.T) {
	spec := l3Spec{
		Name: "shaped", Side: sideIran, Carrier: "udp",
		Addr: "203.0.113.9:9000", Token: "a-token-0123456789abcdefghijklmno",
		LocalIP: "10.20.0.1/30", PeerIP: "10.20.0.2",
		Ports: []string{"443"}, Paths: 4,
	}
	var got config.Config
	if _, err := toml.Decode(spec.render(), &got); err != nil {
		t.Fatalf("the rendered config does not parse: %v", err)
	}
	if got.L3.Paths != 4 {
		t.Errorf("paths = %d, want 4", got.L3.Paths)
	}
}

// A single-socket tunnel writes no key, so an ordinary config does not carry a
// setting that means "the default".
func TestASingleSocketWritesNoPathsKey(t *testing.T) {
	for _, n := range []int{0, 1} {
		spec := l3Spec{
			Name: "plain", Side: sideIran, Carrier: "udp",
			Addr: "203.0.113.9:9000", Token: "a-token-0123456789abcdefghijklmno",
			LocalIP: "10.20.0.1/30", PeerIP: "10.20.0.2",
			Ports: []string{"443"}, Paths: n,
		}
		if out := spec.render(); strings.Contains(out, "paths") {
			t.Errorf("paths=%d rendered a key:\n%s", n, out)
		}
	}
}
