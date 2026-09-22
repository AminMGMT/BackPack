package handlers

import (
	"testing"
	"time"
)

// The relay pool's budget.
//
// Same reasoning as internal/tunnel/l3/bench_gate_test.go: a ceiling generous
// enough not to flake on a shared runner, and an exact allocation count, which
// is the half that is deterministic and the half that matters. This pool exists
// precisely so that a forwarded connection costs no allocation, so the number
// below is zero and it is not a budget — it is the whole point of the file it
// guards.
//
// Measured 2026-09-21, AMD Ryzen 9 4900H, go1.26.0: ~15ns for a get/put pair.
func TestTheRelayPoolStaysFree(t *testing.T) {
	if testing.Short() {
		t.Skip("a measurement")
	}
	if raceEnabled {
		// The race detector multiplies wall-clock time by between five and
		// twenty. No ceiling is meaningful in both modes, so rather than
		// loosening this one until it catches nothing, it runs where the
		// numbers mean something. The allocation counts are checked in the
		// alloc tests, which are unaffected by the detector.
		t.Skip("wall-clock budgets are meaningless under the race detector")
	}
	for _, c := range []struct {
		name    string
		ceiling time.Duration
		run     func(b *testing.B)
	}{
		{"take and return one relay buffer", 200 * time.Nanosecond, BenchmarkRelayBufferPool},
		{"take and return a connection's pair", 400 * time.Nanosecond, BenchmarkRelayBufferPair},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := testing.Benchmark(c.run)
			if res.N == 0 {
				t.Fatal("the benchmark did not run")
			}
			per := time.Duration(res.NsPerOp())
			t.Logf("%s: %v, %d allocs", c.name, per, res.AllocsPerOp())

			if res.AllocsPerOp() != 0 {
				t.Errorf("%s allocates %d times. The pool exists so that a forwarded "+
					"connection costs none; an allocation here is one per connection "+
					"across every transport.", c.name, res.AllocsPerOp())
			}
			if per > c.ceiling {
				t.Errorf("%s costs %v, ceiling %v — more than ten times what it measured "+
					"when the ceiling was set, so something is contending on the pool.",
					c.name, per, c.ceiling)
			}
		})
	}
}
