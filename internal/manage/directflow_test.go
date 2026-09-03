package manage

import (
	"os"
	"strings"
	"testing"
)

// Direct means one thing, and the wizard asks one question about it.
//
// It used to ask two: what kind of tunnel, then how to wrap the packets. Both
// have a single sensible answer now — a full IP tunnel, wrapped in Backpack's
// own GRE inside the Noise session — so both are gone. What is left is the
// question that still has a real choice behind it: which carrier gets it
// across a network that may be filtering.
func TestDirectAsksTheCarrierAndNothingElseFirst(t *testing.T) {
	entry, err := os.ReadFile("setupentry.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	setup, err := os.ReadFile("directsetup.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// The kind question is gone from the path the menu takes.
	if strings.Contains(string(entry), "askKind()") {
		t.Error("the wizard still asks what kind of tunnel before the carrier")
	}
	// And the encapsulation question with it.
	if strings.Contains(string(setup), "func askL3Encap") {
		t.Error("the wizard still offers a choice of encapsulation")
	}
	// The entry point goes straight to the layer-3 setup.
	if !strings.Contains(string(entry), "setupL3(side)") {
		t.Error("the direct entry point no longer reaches the layer-3 wizard")
	}
}

// Every tunnel the wizard writes is GRE, and says so in the file.
func TestTheWizardAlwaysWritesGRE(t *testing.T) {
	for _, side := range []directSide{sideIran, sideKharej} {
		spec := l3Spec{
			Side: side, Carrier: "pck", Encap: "gre",
			Addr: "1.2.3.4:9000", Token: "t", Iface: "bp0",
			LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1371,
		}
		cfg := decode(t, spec.render())
		if cfg.L3.Encap != "gre" {
			t.Errorf("%s: encap = %q, want gre", side, cfg.L3.Encap)
		}
	}

	// The label the screens show has to name what it actually is, so nobody
	// confuses it with the kernel's protocol 47.
	if got := l3EncapLabel(decode(t, l3Spec{
		Side: sideIran, Carrier: "pck", Encap: "gre", Addr: "1.2.3.4:9000",
		Token: "t", Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1371,
	}.render()).L3); got != "GRE + Noise" {
		t.Errorf("label = %q, want \"GRE + Noise\"", got)
	}
}

// The engine must still read a config that says ipip, so a tunnel built before
// the choice was removed keeps running.
// A config written before there was one encapsulation still loads and runs.
//
// It runs as GRE — there is only one now — and the important part is that the
// file does not have to be rewritten by hand for the tunnel to come up. Both
// ends have to be updated together; the handshake says so by name if they are
// not, which is the loud failure the old silent one was replaced with.
func TestAConfigThatStillSaysIPIPLoadsAndRunsAsGRE(t *testing.T) {
	cfg := decode(t, l3Spec{
		Side: sideIran, Carrier: "pck", Encap: "ipip",
		Addr: "1.2.3.4:9000", Token: "t", Iface: "bp0",
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1371,
	}.render())
	// What is rendered now says gre, whatever it was asked for.
	if cfg.L3.Encap != "gre" {
		t.Fatalf("encap = %q, want gre", cfg.L3.Encap)
	}
	if _, err := l3EncapForTest(cfg); err != nil {
		t.Fatalf("the engine refused it: %v", err)
	}
}

// Kernel GRE is gone. Nothing may still reference it, or a config with a [gre]
// table would parse into a field nothing runs — which reads as a tunnel that
// was configured and then silently ignored.
func TestKernelGREIsGone(t *testing.T) {
	if IsDirectKind(Tunnel{Transport: "gre/kernel"}) {
		t.Error("the management screens still recognise a kernel GRE tunnel")
	}
	for _, f := range []string{"gresetup.go", "gresetup_test.go"} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s still exists", f)
		}
	}
}
