//go:build linux

package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/instanceid"
)

type recordingRunner struct {
	mu    sync.Mutex
	calls []string
	out   map[string]string
	fail  string
}

func (r *recordingRunner) Combined(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if r.fail != "" && strings.Contains(call, r.fail) {
		return nil, errors.New("injected command failure")
	}
	return []byte(r.out[call]), nil
}

func directTestConfig() *config.Config {
	return &config.Config{
		Engine: config.EngineIPTables,
		Forward: config.ForwardConfig{Mappings: []config.ForwardMapping{{
			ListenAddress: "0.0.0.0", ListenPorts: "443-445",
			TargetAddress: "192.0.2.8", TargetPorts: "8443-8445",
			Protocols: []string{"tcp", "udp"},
		}}},
	}
}

func TestExpandPreservesPortOffsetAndProtocol(t *testing.T) {
	rules, err := expand(directTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 6 {
		t.Fatalf("got %d expanded rules, want 6", len(rules))
	}
	for _, r := range rules {
		if int(r.targetPort)-int(r.listenPort) != 8000 {
			t.Fatalf("offset changed for %#v", r)
		}
		if r.family != "ipv4" || (r.proto != "tcp" && r.proto != "udp") {
			t.Fatalf("unexpected expansion: %#v", r)
		}
	}
}

func TestGenerationNamesAreStableBoundedAndSeparated(t *testing.T) {
	id := instanceid.Identity{InstanceID: "42a27077-dfa3-45dc-a4a3-fc78f622c725", Connmark: 7}
	g1, err := makeGeneration(id, 12, []string{"ipv4", "ipv6"})
	if err != nil {
		t.Fatal(err)
	}
	g2, _ := makeGeneration(id, 12, []string{"ipv4", "ipv6"})
	for family, chains := range g1.chains {
		for purpose, name := range chains {
			if name != g2.chains[family][purpose] {
				t.Fatalf("chain name is not deterministic: %q != %q", name, g2.chains[family][purpose])
			}
			if len(name) > 28 {
				t.Fatalf("chain %q exceeds iptables limit", name)
			}
		}
	}
	if g1.chains["ipv4"]["N"] == g1.chains["ipv6"]["N"] || g1.chains["ipv4"]["N"] == g1.chains["ipv4"]["F"] {
		t.Fatal("family and purpose must produce distinct chains")
	}
}

func TestDetachedRulesArePreciselyMarkedAndAccounted(t *testing.T) {
	old := nfRunner
	recorder := &recordingRunner{out: map[string]string{}}
	nfRunner = recorder
	t.Cleanup(func() { nfRunner = old })

	id := instanceid.Identity{InstanceID: "4c33bb4a-2a7a-4d3b-85f2-e65e77302289", Connmark: 0x10203}
	g, _ := makeGeneration(id, 1, []string{"ipv4"})
	rules, _ := expand(&config.Config{Forward: config.ForwardConfig{Mappings: []config.ForwardMapping{{
		ListenAddress: "0.0.0.0", ListenPorts: "443", TargetAddress: "192.0.2.8", TargetPorts: "8443", Protocols: []string{"tcp"},
	}}}})
	tools := map[string]familyTools{"ipv4": {family: "ipv4", cmd: "iptables", save: "iptables-save", restore: "iptables-restore"}}
	if err := buildDetached(context.Background(), id, g, tools, rules); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(recorder.calls, "\n")
	for _, want := range []string{
		"--set-xmark 0x10203/0xffffffff",
		"--comment backpack:" + id.InstanceID + ":dnat:0001",
		"--to-destination 192.0.2.8:8443",
		"--comment backpack:" + id.InstanceID + ":acct-rx:0001",
		"--ctstate NEW,ESTABLISHED,RELATED",
		"--comment backpack:" + id.InstanceID + ":acct-tx:0001",
		"--ctstate ESTABLISHED,RELATED",
		"--comment backpack:" + id.InstanceID + ":masquerade:0001",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing rule fragment %q\ncommands:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, " OUTPUT ") {
		t.Fatal("direct forwarding must not install OUTPUT rules")
	}
}

func TestCounterDeltaSurvivesResetWithoutDoubleCounting(t *testing.T) {
	total := kernelCount{}
	first := kernelCount{RXBytes: 100, TXBytes: 50, RXPackets: 10, TXPackets: 5}
	addDelta(&total, kernelCount{}, first)
	addDelta(&total, first, first) // repeated scrape
	addDelta(&total, first, kernelCount{RXBytes: 20, TXBytes: 8, RXPackets: 2, TXPackets: 1})
	if total != (kernelCount{RXBytes: 120, TXBytes: 58, RXPackets: 12, TXPackets: 6}) {
		t.Fatalf("unexpected cumulative count after reset: %#v", total)
	}
}

func TestAccountingParserDoesNotCountDNATOrMasquerade(t *testing.T) {
	id := instanceid.Identity{InstanceID: "5ef3576d-b7f6-4c6e-854c-c4550db0e124"}
	old := nfRunner
	filter := "[4:400] -A B4F -m comment --comment backpack:" + id.InstanceID + ":acct-rx:0002 -j ACCEPT\n" +
		"[2:100] -A B4F -m comment --comment backpack:" + id.InstanceID + ":acct-tx:0002 -j ACCEPT\n" +
		"[99:9999] -A B4F -m comment --comment backpack:" + id.InstanceID + ":dnat:0002 -j DNAT\n"
	recorder := &recordingRunner{out: map[string]string{"iptables-save -c -t filter": filter}}
	nfRunner = recorder
	t.Cleanup(func() { nfRunner = old })
	got, err := scrape(context.Background(), id, map[string]familyTools{"ipv4": {save: "iptables-save"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["0002"] != (kernelCount{RXBytes: 400, TXBytes: 100, RXPackets: 4, TXPackets: 2}) {
		t.Fatalf("wrong accounting counters: %#v", got["0002"])
	}
}

func TestConnmarkCollisionUsesNumericTokenNotSubstring(t *testing.T) {
	if ruleUsesConnmark("-A X -m connmark --mark 0x10/0xffffffff -j ACCEPT", 0x1) {
		t.Fatal("0x1 must not collide with 0x10")
	}
	if !ruleUsesConnmark("-A X -j CONNMARK --set-xmark 0x1/0xffffffff", 0x1) {
		t.Fatal("exact set-xmark was not detected")
	}
}

func TestNativeNFTConflictReturnsActionableRule(t *testing.T) {
	ruleset := map[string]any{"nftables": []any{
		map[string]any{"rule": map[string]any{"family": "ip", "table": "nat", "chain": "prerouting", "expr": []any{map[string]any{"dnat": map[string]any{"addr": "192.0.2.8"}}}}},
	}}
	got := nativeDNATConflict(ruleset, "ipv4", "owned")
	if !strings.Contains(got, "prerouting") || !strings.Contains(got, "dnat") {
		t.Fatalf("native nft conflict is not actionable: %q", got)
	}
	ruleset["nftables"] = []any{map[string]any{"rule": map[string]any{"comment": "backpack:owned:dnat:1", "expr": []any{map[string]any{"dnat": map[string]any{"addr": "192.0.2.8"}}}}}}
	if got := nativeDNATConflict(ruleset, "ipv4", "owned"); got != "" {
		t.Fatalf("owned rule reported as native conflict: %s", got)
	}
}

func TestHooksActivateIngressLastAndRollbackBothFamilies(t *testing.T) {
	old := nfRunner
	recorder := &recordingRunner{out: map[string]string{}}
	nfRunner = recorder
	t.Cleanup(func() { nfRunner = old })

	id := instanceid.Identity{InstanceID: "46b06f94-b61e-4569-b5d4-5472b97fdcff", Connmark: 77}
	g, _ := makeGeneration(id, 3, []string{"ipv4", "ipv6"})
	tools := map[string]familyTools{
		"ipv4": {family: "ipv4", cmd: "iptables"},
		"ipv6": {family: "ipv6", cmd: "ip6tables"},
	}
	if err := installHooks(context.Background(), id, g, tools); err != nil {
		t.Fatal(err)
	}
	for _, family := range []string{"iptables", "ip6tables"} {
		var familyCalls []string
		for _, call := range recorder.calls {
			if strings.HasPrefix(call, family+" ") {
				familyCalls = append(familyCalls, call)
			}
		}
		if len(familyCalls) != 3 || !strings.Contains(familyCalls[2], " PREROUTING ") {
			t.Fatalf("%s ingress hook was not activated last: %#v", family, familyCalls)
		}
	}

	recorder.calls = nil
	rollbackGeneration(context.Background(), id, g, tools)
	joined := strings.Join(recorder.calls, "\n")
	for _, chain := range []string{g.chains["ipv4"]["N"], g.chains["ipv4"]["F"], g.chains["ipv4"]["P"], g.chains["ipv6"]["N"], g.chains["ipv6"]["F"], g.chains["ipv6"]["P"]} {
		if !strings.Contains(joined, " -X "+chain) {
			t.Errorf("rollback did not remove %s", chain)
		}
	}
}

func TestForwardStatePersistsSessionAndCumulative(t *testing.T) {
	path := t.TempDir() + "/direct.toml"
	id := instanceid.Identity{InstanceID: "77f40d73-bddd-4a84-87fb-004a0a5af309", Connmark: 9}
	want := forwardState{InstanceID: id.InstanceID, Generation: 4, Last: map[string]kernelCount{"0004": {RXBytes: 11}}, Session: kernelCount{RXBytes: 20, TXPackets: 2}, Cumulative: kernelCount{RXBytes: 120, TXPackets: 12}}
	if err := saveForwardState(path, want); err != nil {
		t.Fatal(err)
	}
	got := loadForwardState(path, id)
	if got.Session != want.Session || got.Cumulative != want.Cumulative || got.Last["0004"] != want.Last["0004"] {
		t.Fatalf("state round-trip changed counters: %#v", got)
	}
}

func TestDetachedCollisionNeverRollsBackPreexistingChain(t *testing.T) {
	old := nfRunner
	id := instanceid.Identity{InstanceID: "b753e744-bd51-4714-b57e-57e78e0220a0", Connmark: 55}
	g, _ := makeGeneration(id, 1, []string{"ipv4"})
	collision := g.chains["ipv4"]["N"]
	recorder := &recordingRunner{out: map[string]string{}, fail: " -N " + collision}
	nfRunner = recorder
	t.Cleanup(func() { nfRunner = old })
	tools := map[string]familyTools{"ipv4": {family: "ipv4", cmd: "iptables"}}
	if err := buildDetached(context.Background(), id, g, tools, nil); err == nil {
		t.Fatal("injected chain collision was accepted")
	}
	joined := strings.Join(recorder.calls, "\n")
	if strings.Contains(joined, " -F "+collision) || strings.Contains(joined, " -X "+collision) {
		t.Fatalf("rollback touched the preexisting colliding chain:\n%s", joined)
	}
}
