package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/optimize"
)

// The engine tunes the kernel on every start, and its table disagreed with the
// one Optimize writes.
//
// Four settings, four times the same shape. Optimize writes a value, the
// operator has deliberately run it, and then the next tunnel start puts a
// different one back — with nothing anywhere saying so. ip_local_port_range was
// the visible one, because losing a service's port is a thing people notice;
// net.core.rmem_default (16 MB down to 1 MB), wmem_default (the same) and
// tcp_notsent_lowat (128 KB down to 32 KB) were not visible at all.
//
// There is one table now. These check that it stays one table.
func TestTheEngineTakesItsTuningFromOptimize(t *testing.T) {
	src := tuningSource(t)
	for _, key := range []string{
		"net.core.rmem_default", "net.core.wmem_default",
		"net.ipv4.tcp_notsent_lowat", "net.core.somaxconn",
	} {
		if strings.Contains(src, key) {
			t.Errorf("the engine carries its own value for %s — that is the second copy "+
				"the two tables drifted between", key)
		}
	}
	if !strings.Contains(src, "optimize.EngineStartupTuning()") {
		t.Error("the engine no longer reads Optimize's table")
	}
}

// What a starting tunnel applies is a subset of what Optimize applies, and it
// has to be a real subset: every value identical, and nothing in it that is
// machine policy rather than socket tuning.
func TestTheEnginesTuningAgreesWithOptimize(t *testing.T) {
	engine := optimize.EngineStartupTuning()
	if len(engine) == 0 {
		t.Fatal("a starting tunnel applies no tuning at all")
	}

	full := map[string]string{}
	for _, kv := range optimize.FullTuning() {
		full[kv[0]] = kv[1]
	}
	for _, kv := range engine {
		want, ok := full[kv[0]]
		if !ok {
			t.Errorf("the engine sets %s, which Optimize does not — the two tables have "+
				"separated again", kv[0])
			continue
		}
		if want != kv[1] {
			t.Errorf("the engine sets %s=%s and Optimize sets it to %s", kv[0], kv[1], want)
		}
	}

	// Machine policy is Optimize's alone: installing a tunnel is not consent to
	// have the whole box retuned.
	for _, kv := range engine {
		switch kv[0] {
		case "net.ipv4.ip_local_port_range",
			"net.ipv4.tcp_congestion_control",
			"net.core.default_qdisc",
			"net.ipv4.ip_forward":
			t.Errorf("a starting tunnel sets %s, which changes how everything else on "+
				"the machine behaves", kv[0])
		}
	}
}

// tuningSource reads the file rather than the running table because what is
// being checked is that there is no second table in it.
func tuningSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("optimization.go")
	if err != nil {
		t.Fatalf("optimization.go: %v", err)
	}
	var live []string
	for _, line := range strings.Split(string(b), "\n") {
		if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "//") {
			live = append(live, line)
		}
	}
	return strings.Join(live, "\n")
}
