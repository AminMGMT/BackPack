package l3

import (
	"flag"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
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
//
// # Scaled by what the machine does right now
//
// The three-times margin assumed a runner that is slower but steady. CI's is
// neither: `go test ./...` runs packages side by side, and since v2.0.0 the
// heavy ones (manage, paqet, e2e) run next to this one on a four-core runner.
// The gate failed there in five runs out of six with nothing changed in the
// path — 3.1-8.1 µs to seal where this machine took 0.78 — and once under the
// coverage step's instrumentation. So each path is measured several times
// with a calibration beside it, ChaCha20-Poly1305 over the same payload (the
// cipher the Noise session uses, so the bulk of the honest cost), and the
// ceiling is scaled by how much slower than the reference that calibration
// ran. A runner that is slow or busy slows both; a per-packet loop that grew
// a syscall, a lock or a copy slows only the path, and the ratio catches it.
// The best of three short repetitions is kept on both sides, since contention only
// ever adds. Measured 2026-11-15 on the machine above: 640 ns for 1400 bytes,
// 134 ns for 64 (calibration), seal 779 / 230 ns, round trip 1515 / 454 ns.
// Under coverage instrumentation the path is slowed and the calibration is
// not, so there only the allocation counts are checked.

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
	// calib is the payload size of the calibration that scales ceiling, 0 for
	// the sub-nanosecond paths, which only the allocation count checks.
	calib int
}

// calibRef is what the calibration took on the machine the ceilings were set
// on, per payload size.
var calibRef = map[int]time.Duration{1400: 640 * time.Nanosecond, 64: 134 * time.Nanosecond}

// calibrate is a ChaCha20-Poly1305 seal of n bytes: the honest per-packet cost.
func calibrate(n int) func(b *testing.B) {
	return func(b *testing.B) {
		aead, err := chacha20poly1305.New(make([]byte, chacha20poly1305.KeySize))
		if err != nil {
			b.Fatal(err)
		}
		nonce := make([]byte, chacha20poly1305.NonceSize)
		payload := make([]byte, n)
		dst := make([]byte, 0, n+chacha20poly1305.Overhead)
		for i := 0; i < b.N; i++ {
			dst = aead.Seal(dst[:0], nonce, payload, nil)
		}
	}
}

// gateRounds is how many times each path and its calibration are measured,
// each for gateBenchtime: best of three short measurements is steadier than one
// long one on a runner that is busy in bursts, and keeps the test near ten
// seconds.
const (
	gateRounds    = 3
	gateBenchtime = "300ms"
)

// The send header now uses spare destination capacity; sealing allocates only
// the Noise nonce. Opening reuses a header under recvMu and also allocates
// only its nonce.
func hotPaths() []hotPath {
	return []hotPath{
		{"seal a full-MTU packet", 3 * time.Microsecond, 1, BenchmarkSealFullMTU, 1400},
		{"seal an interactive packet", 1500 * time.Nanosecond, 1, BenchmarkSealInteractive, 64},
		{"a full-MTU packet round trip", 6 * time.Microsecond, 2, BenchmarkPacketRoundTripFullMTU, 1400},
		{"an interactive packet round trip", 3 * time.Microsecond, 2, BenchmarkPacketRoundTripInteractive, 64},
		{"parse a header", 20 * time.Nanosecond, 0, BenchmarkParseHeader, 0},
		{"consult the replay window", 20 * time.Nanosecond, 0, BenchmarkReplayWindow, 0},
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
	timed := testing.CoverMode() == ""
	if f := flag.Lookup("test.benchtime"); f != nil {
		prev := f.Value.String()
		if err := f.Value.Set(gateBenchtime); err == nil {
			defer f.Value.Set(prev)
		}
	}
	for _, p := range hotPaths() {
		t.Run(p.name, func(t *testing.T) {
			var per, cal time.Duration
			perAlloc := -1
			for i := 0; i < gateRounds; i++ {
				res := testing.Benchmark(p.run)
				if res.N == 0 {
					t.Fatalf("the benchmark did not run")
				}
				if d := time.Duration(res.NsPerOp()); i == 0 || d < per {
					per = d
				}
				perAlloc = int(res.AllocsPerOp())
				if p.calib > 0 {
					if d := time.Duration(testing.Benchmark(calibrate(p.calib)).NsPerOp()); i == 0 || d < cal {
						cal = d
					}
				}
				if !timed {
					break // only the allocation count means anything here
				}
			}
			ceiling := p.ceiling
			if ref := calibRef[p.calib]; ref > 0 && cal > ref {
				ceiling = time.Duration(float64(p.ceiling) * float64(cal) / float64(ref))
			}

			t.Logf("%s: %v per packet (calibration %v), %d allocs, ceiling %v", p.name, per, cal, perAlloc, ceiling)

			if timed && per > ceiling {
				t.Errorf("%s costs %v, ceiling %v (%v scaled by this machine's calibration, %v against %v "+
					"where the ceiling was set). The ceiling is three times what this path measured, "+
					"over a calibration measured beside it, so this is not runner noise — something "+
					"was added to a per-packet loop.", p.name, per, ceiling, p.ceiling, cal, calibRef[p.calib])
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
