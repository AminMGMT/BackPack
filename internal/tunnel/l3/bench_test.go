package l3

import (
	"testing"
)

// The layer-3 data path, per packet.
//
// These sit beside the allocation budgets in alloc_test.go rather than
// replacing them. A budget catches the regression that matters most — work
// appearing on a path that was allocation-free — and it says nothing about how
// fast the path is. These say that, and the CI gate in bench_gate_test.go turns
// a number into a check.
//
// Sizes are the two that actually occur: a full-MTU packet, which is what a
// bulk transfer is made of, and a small one, which is what an interactive
// session is made of. The per-packet cost of the small one is what a tunnel
// carrying SSH or a game feels.

func benchSeal(b *testing.B, size int) {
	initiator, _ := handshakePair(b, "a-benchmark-token-0123456789abc")
	payload := make([]byte, size)
	dst := make([]byte, 0, size+256)
	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := initiator.seal(dst, payload)
		if err != nil {
			b.Fatal(err)
		}
		dst = out[:0]
	}
}

func BenchmarkSealFullMTU(b *testing.B)     { benchSeal(b, 1400) }
func BenchmarkSealInteractive(b *testing.B) { benchSeal(b, 64) }

func benchRoundTrip(b *testing.B, size int) {
	initiator, responder := handshakePair(b, "a-benchmark-token-0123456789abc")
	payload := make([]byte, size)
	sealed := make([]byte, 0, size+256)
	opened := make([]byte, 0, size+256)
	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := initiator.seal(sealed, payload)
		if err != nil {
			b.Fatal(err)
		}
		h, body, err := parseHeader(out)
		if err != nil {
			b.Fatal(err)
		}
		plain, err := responder.open(opened, h, body)
		if err != nil {
			b.Fatal(err)
		}
		sealed, opened = out[:0], plain[:0]
	}
}

// The whole per-packet cost of the tunnel: seal on one side, parse and open on
// the other. This is the number that divides into a throughput figure.
func BenchmarkPacketRoundTripFullMTU(b *testing.B)     { benchRoundTrip(b, 1400) }
func BenchmarkPacketRoundTripInteractive(b *testing.B) { benchRoundTrip(b, 64) }

// Parsing on its own, because every datagram that arrives pays it — including
// the ones that turn out to be a scanner's.
func BenchmarkParseHeader(b *testing.B) {
	buf := make([]byte, headerLen+1400)
	buf[0] = typeData
	b.SetBytes(int64(len(buf)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := parseHeader(buf); err != nil {
			b.Fatal(err)
		}
	}
}

// The replay window is consulted once per packet and is the one piece of
// per-packet state with a lock on it.
func BenchmarkReplayWindow(b *testing.B) {
	var w replayWindow
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.accept(uint64(i))
	}
}
