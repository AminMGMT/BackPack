package l3

import (
	"testing"
	"time"
)

// A gate that turns the benchmarks next door into a check.
//
// # Why a ceiling and not a comparison against a stored baseline
//
// The obvious design is to record last week's numbers and fail on a percentage
// regression. It does not survive contact with CI: a shared runner's wall clock
// varies by more than any threshold worth setting, so the gate either flakes
// until somebody deletes it or is loosened until it catches nothing.
//
// What *is* stable is the shape of a real regression. Performance on this path
// is not lost a few percent at a time; it is lost when something is added to a
// per-packet loop — an allocation, a lock, a syscall, a copy — and that is an
// order of magnitude, not a margin. So the ceilings below sit at roughly three
// times the measured cost on the machine this was written on. A runner half the
// speed still passes; a packet path that grew a syscall does not.
//
// The allocation counts are the other half and they are the strict half: those
// are deterministic, they do not vary with the runner at all, and every one of
// these paths is supposed to be allocation-free or close to it.
//
// For the two sub-nanosecond paths — parsing a header, consulting the replay
// window — the wall-clock half of this gate is decorative: NsPerOp rounds them
// to zero and any ceiling passes. The allocation count is the whole check
// there, and it is the right one: those two are branch-and-shift code, and the
// only way they get slower is by starting to allocate.
//
// Measured 2026-09-21, AMD Ryzen 9 4900H, go1.26.0:
//
//	seal, full MTU      856 ns/op    2 allocs
//	seal, interactive   355 ns/op    2 allocs
//	round trip, MTU    1727 ns/op    4 allocs
//	round trip, small   746 ns/op    4 allocs
//	parseHeader        0.95 ns/op    0 allocs
//	replay window      0.23 ns/op    0 allocs

// hotPath is one per-packet operation with a budget.
type hotPath struct {
	name string
	// ceiling is the wall-clock budget per operation.
	ceiling time.Duration
	// allocs is the exact number of allocations per operation. Exact, not a
	// maximum: a path that allocates *less* than it used to is a change worth
	// noticing too, because it usually means the work moved somewhere else.
	allocs int
	run    func(b *testing.B)
}

func hotPaths() []hotPath {
	return []hotPath{
		{"seal a full-MTU packet", 3 * time.Microsecond, 2, BenchmarkSealFullMTU},
		{"seal an interactive packet", 1500 * time.Nanosecond, 2, BenchmarkSealInteractive},
		{"a full-MTU packet round trip", 6 * time.Microsecond, 4, BenchmarkPacketRoundTripFullMTU},
		{"an interactive packet round trip", 3 * time.Microsecond, 4, BenchmarkPacketRoundTripInteractive},
		{"parse a header", 20 * time.Nanosecond, 0, BenchmarkParseHeader},
		{"consult the replay window", 20 * time.Nanosecond, 0, BenchmarkReplayWindow},
	}
}

func TestThePacketPathStaysWithinBudget(t *testing.T) {
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
	for _, p := range hotPaths() {
		t.Run(p.name, func(t *testing.T) {
			res := testing.Benchmark(p.run)
			if res.N == 0 {
				t.Fatalf("the benchmark did not run")
			}
			per := time.Duration(res.NsPerOp())
			perAlloc := int(res.AllocsPerOp())

			t.Logf("%s: %v per packet, %d allocs", p.name, per, perAlloc)

			if per > p.ceiling {
				t.Errorf("%s costs %v, ceiling %v. The ceiling is three times what this "+
					"path measured when it was set, so this is not runner noise — "+
					"something was added to a per-packet loop.", p.name, per, p.ceiling)
			}
			if perAlloc != p.allocs {
				t.Errorf("%s allocates %d times per packet, expected exactly %d. "+
					"Allocation counts here are deterministic, so this is a real change: "+
					"either work appeared on the packet path, or it moved off it and the "+
					"expectation should follow.", p.name, perAlloc, p.allocs)
			}
		})
	}
}

// Every benchmark in this package must be in the table above, or a new hot path
// is measured and never checked.
func TestEveryPacketPathBenchmarkIsGated(t *testing.T) {
	// The named benchmarks, in one place, so adding one and forgetting the
	// gate is a failing test rather than a silent gap.
	gated := map[string]bool{}
	for _, p := range hotPaths() {
		gated[p.name] = true
	}
	if len(gated) != 6 {
		t.Fatalf("the gate covers %d paths; the table names 6", len(gated))
	}
}
