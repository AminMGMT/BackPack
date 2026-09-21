package l3

import (
	"testing"
)

// The data path must not allocate per packet.
//
// The codebase is careful about this — pooled relay buffers, reused slices
// whose capacity settles rather than reallocating, a channel depth cut by three
// orders of magnitude after it was measured — and nothing enforced it. The
// consequence was a `time.After` inside the forwarded-UDP copy loop, allocating
// a sixty-second timer for every packet and holding it for its full duration.
// It was found by reading the code, months after it was written, in a loop
// nobody had profiled.
//
// testing.AllocsPerRun answers the question deterministically, so this is an
// ordinary test rather than a benchmark whose output somebody has to read. A
// regression fails the build instead of waiting for an audit.
//
// The budgets below are what the code does today, not aspirations. Raising one
// is a decision to be argued for in a diff; lowering one is free.

// sealAllocBudget and openAllocBudget are per-packet allocation counts for the
// layer-3 data path.
//
// Both are non-zero because the AEAD is handed a destination slice and appends
// to it: the ciphertext lands in the caller's buffer, but the noise.Cipher
// interface boxes its arguments on the way in. Pinning the current figure is
// what matters — the failure this guards against is one going from 2 to 200.
const (
	sealAllocBudget = 3
	openAllocBudget = 3
)

func TestSealingAPacketDoesNotAllocatePerPacket(t *testing.T) {
	initiator, _ := handshakePair(t, "an-allocation-budget-token-0123")

	payload := make([]byte, 1400) // a full-sized inner packet
	dst := make([]byte, 0, 2048)  // the caller's reused buffer, as the pump has

	got := testing.AllocsPerRun(200, func() {
		out, err := initiator.seal(dst, payload)
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		dst = out[:0] // exactly what pumpFromTUN does with the result
	})
	if int(got) > sealAllocBudget {
		t.Errorf("seal allocates %.0f times per packet, budget %d.\n"+
			"At a few thousand packets a second that is the difference between a "+
			"tunnel the collector ignores and one it competes with. If this increase "+
			"is deliberate, raise sealAllocBudget in the same diff and say why.",
			got, sealAllocBudget)
	}
}

func TestOpeningAPacketDoesNotAllocatePerPacket(t *testing.T) {
	initiator, responder := handshakePair(t, "an-allocation-budget-token-0123")

	payload := make([]byte, 1400)
	plain := make([]byte, 0, 2048)

	// A fresh sealed datagram per iteration, prepared outside the measured
	// closure so the cost of making one is not charged to opening it. The
	// replay window refuses a counter twice, so each needs its own.
	const runs = 200
	sealed := make([][]byte, runs+1)
	for i := range sealed {
		out, err := initiator.seal(nil, payload)
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		sealed[i] = out
	}

	i := 0
	got := testing.AllocsPerRun(runs, func() {
		h, body, err := parseHeader(sealed[i])
		i++
		if err != nil {
			t.Fatalf("parseHeader: %v", err)
		}
		out, err := responder.open(plain, h, body)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if cap(out) > cap(plain) {
			plain = out[:0]
		}
	})
	if int(got) > openAllocBudget {
		t.Errorf("open allocates %.0f times per packet, budget %d. See the note on "+
			"sealAllocBudget.", got, openAllocBudget)
	}
}

// Benchmarks for a human looking at throughput rather than at regressions.
func BenchmarkSeal(b *testing.B) {
	initiator, _ := handshakePair(b, "an-allocation-budget-token-0123")
	payload := make([]byte, 1400)
	dst := make([]byte, 0, 2048)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out, err := initiator.seal(dst, payload)
		if err != nil {
			b.Fatal(err)
		}
		dst = out[:0]
	}
}
