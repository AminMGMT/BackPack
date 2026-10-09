package network

import (
	"net"
	"testing"
)

// The stealth record layer allocated twice for every record it sent.
//
// The cipher was handed a nil destination, so it allocated its output, and the
// framing then did append(hdr, msg...) — a second allocation and a copy of the
// whole record, up to 64 KB. Once per record, on the hot path of the one
// transport whose reason for existing is to be indistinguishable from ordinary
// traffic, in a package that fights hard for exactly this elsewhere.
//
// The count is asserted rather than a rate: a benchmark measures the machine it
// runs on, and this is a property of the code.
func TestTheRecordLayerDoesNotAllocatePerRecord(t *testing.T) {
	const token = "a-real-looking-tunnel-token-0123456789"
	a, b, cerr, serr := noisePair(t, token, token)
	if cerr != nil || serr != nil {
		t.Fatalf("handshake: client %v, server %v", cerr, serr)
	}
	defer a.Close()
	defer b.Close()

	// AllocsPerRun counts allocations on every goroutine. A background peer
	// therefore charges its decryption nonce and growing receive buffers to
	// this writer, and makes this guard depend on scheduling. Complete the real
	// handshake, then discard this direction's encrypted wire records locally.
	// TestStealthRoundTrip separately verifies their decryption over TCP.
	w := a.(*noiseConn)
	w.Conn = noiseAllocationSink{Conn: w.Conn}

	payload := make([]byte, 8*1024)
	// Warm: the first records size the reused buffers, which is an allocation
	// this test is not about.
	if _, err := a.Write(make([]byte, noisePaddedMaxPayload+noiseMaxPad)); err != nil {
		t.Fatalf("warm write: %v", err)
	}

	got := testing.AllocsPerRun(50, func() {
		if _, err := a.Write(payload); err != nil {
			t.Fatalf("write: %v", err)
		}
	})
	// The cipher's nonce currently costs one allocation. A fresh frame or
	// padding buffer per record must fail this guard, rather than hiding in it.
	if got > 1 {
		t.Errorf("a record costs %.1f allocations — the record buffer is being rebuilt "+
			"for every one", got)
	}
	t.Logf("%.1f allocations per record", got)
}

type noiseAllocationSink struct{ net.Conn }

func (noiseAllocationSink) Write(p []byte) (int, error) { return len(p), nil }
