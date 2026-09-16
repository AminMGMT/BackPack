package network

import (
	"io"
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

	// Drained in the background, or Write blocks once the socket buffer fills.
	go io.Copy(io.Discard, b)

	payload := make([]byte, 8*1024)
	// Warm: the first records size the reused buffers, which is an allocation
	// this test is not about.
	for i := 0; i < 8; i++ {
		if _, err := a.Write(payload); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	got := testing.AllocsPerRun(50, func() {
		if _, err := a.Write(payload); err != nil {
			t.Fatalf("write: %v", err)
		}
	})
	// Slack for whatever the socket and the cipher do internally; what must not
	// be there is a fresh buffer for the record itself, twice.
	if got > 2 {
		t.Errorf("a record costs %.1f allocations — the record buffer is being rebuilt "+
			"for every one", got)
	}
	t.Logf("%.1f allocations per record", got)
}
