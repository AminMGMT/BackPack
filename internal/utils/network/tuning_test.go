package network

import "testing"

// The default must stay "do not pin".
//
// Pinning SO_RCVBUF/SO_SNDBUF locks the socket buffer and switches off the
// kernel's window auto-tuning, which costs a large multiple of the throughput
// on a fast uplink. This was the behaviour once, and it is the kind of default
// that can be flipped back by accident while looking like a tuning improvement,
// so it is asserted rather than left to review.
func TestTCPBuffersAreNotPinnedByDefault(t *testing.T) {
	if PinTCPBuffers() {
		t.Fatal("PinTCPBuffers must default to false so the kernel can auto-tune the TCP window")
	}
}

// BBR is requested per socket so the tunnel gets it even where the system
// default was never changed.
func TestDefaultCongestionIsBBR(t *testing.T) {
	if TCPCongestion != "bbr" {
		t.Fatalf("TCPCongestion = %q, want bbr", TCPCongestion)
	}
}
