package mssclamp

import (
	"strings"
	"testing"
)

// The command line, not the rule description.
//
// This is the test that was missing. The old code kept the whole command in one
// slice and wrote the verb over index 2 — which held the chain — so every
// invocation came out as "iptables -t mangle -A -o bp0 ..." with no chain at
// all. iptables refused all of them and the refusal was logged at debug level,
// so the clamp was absent from every machine while the code that built it
// passed its tests: they only ever inspected the description, never the argv
// that would actually be run.
func TestTheCommandLineIsWellFormed(t *testing.T) {
	rules := Rules("l3", "bp0", 1400, 0)
	if len(rules) != 4 {
		t.Fatalf("got %d rules, want 4 (two chains x two families)", len(rules))
	}

	for _, r := range rules {
		for _, verb := range []string{"-A", "-D", "-C"} {
			args := r.Args(verb)

			// iptables takes: -t <table> <verb> <chain> <match...> <target...>
			if len(args) < 5 {
				t.Fatalf("%s %v: too short to be a command", r.Cmd, args)
			}
			if args[0] != "-t" || args[1] != r.Table {
				t.Errorf("%s: does not start with the table: %v", r.Cmd, args[:2])
			}
			if args[2] != verb {
				t.Errorf("%s: verb slot holds %q, want %q", r.Cmd, args[2], verb)
			}
			// The one that broke: the chain must survive the verb.
			if args[3] != r.Chain {
				t.Errorf("%s %s: chain slot holds %q, want %q — the verb displaced it",
					r.Cmd, verb, args[3], r.Chain)
			}
			if r.Chain != "FORWARD" && r.Chain != "OUTPUT" {
				t.Errorf("unexpected chain %q", r.Chain)
			}

			// And nothing may be lost from the match.
			line := strings.Join(args, " ")
			for _, want := range []string{"-o bp0", "-p tcp", "--tcp-flags SYN,RST SYN", "-j TCPMSS", "--set-mss"} {
				if !strings.Contains(line, want) {
					t.Errorf("%s %s: missing %q in %q", r.Cmd, verb, want, line)
				}
			}
		}
	}
}

// Add and delete must describe the same rule, or the sweep cannot find what the
// add created and rules accumulate — which is the fault this whole shape exists
// to prevent.
func TestAddAndDeleteMatchExactly(t *testing.T) {
	for _, r := range Rules("gre", "gre1", 1400, 0) {
		add := r.Args("-A")
		del := r.Args("-D")
		if len(add) != len(del) {
			t.Fatalf("add and delete differ in length: %v vs %v", add, del)
		}
		for i := range add {
			if i == 2 {
				continue // the verb, which is the only difference
			}
			if add[i] != del[i] {
				t.Errorf("add and delete differ at %d: %q vs %q", i, add[i], del[i])
			}
		}
	}
}

// Each tunnel kind tags its own rules, so two kinds on one host cannot sweep
// each other's away.
func TestRulesAreTaggedPerKindAndInterface(t *testing.T) {
	l3 := strings.Join(Rules("l3", "bp0", 1400, 0)[0].Args("-A"), " ")
	gre := strings.Join(Rules("gre", "gre1", 1400, 0)[0].Args("-A"), " ")

	if !strings.Contains(l3, "backpack-l3-mss-bp0") {
		t.Errorf("l3 rule is not tagged: %s", l3)
	}
	if !strings.Contains(gre, "backpack-gre-mss-gre1") {
		t.Errorf("gre rule is not tagged: %s", gre)
	}
	if strings.Contains(l3, "gre1") || strings.Contains(gre, "bp0") {
		t.Error("the two kinds' rules name each other's interfaces")
	}
}

func TestValueIsDerivedFromTheMTU(t *testing.T) {
	if got := For(0, 1400, OverheadV4); got != 1360 {
		t.Errorf("automatic IPv4 clamp for mtu 1400 = %d, want 1360", got)
	}
	if got := For(0, 1400, OverheadV6); got != 1340 {
		t.Errorf("automatic IPv6 clamp for mtu 1400 = %d, want 1340", got)
	}
	if got := For(1208, 1400, OverheadV4); got != 1208 {
		t.Errorf("explicit clamp = %d, want 1208", got)
	}
}

// An MTU too small to leave room for a segment must produce no rule at all,
// rather than one with a nonsensical size.
func TestNoRuleWhenNothingFits(t *testing.T) {
	for _, r := range Rules("l3", "bp0", 30, 0) {
		if r.MSS <= 0 {
			t.Fatalf("a rule was built with mss %d", r.MSS)
		}
	}
}
