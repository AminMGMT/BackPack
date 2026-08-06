package network

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
)

// The packet crafting must round-trip — a shim this code builds must be one it
// accepts — and the L4 checksum must be the value a receiver recomputes as zero,
// which is the only proof the header is well formed without a kernel to send it.

func verifyChecksum(t *testing.T, src, dst net.IP, proto int, segment []byte) {
	t.Helper()
	// Recomputing the ones-complement sum over a segment whose checksum field is
	// already filled must yield zero for a valid checksum.
	if got := l4Checksum(src, dst, proto, segment); got != 0 {
		t.Fatalf("checksum does not verify to zero, got %#04x", got)
	}
}

func TestSpoofUDPShimRoundTrip(t *testing.T) {
	src := net.ParseIP("185.143.234.120")
	dst := net.ParseIP("38.87.117.94")
	const port = 40000
	payload := []byte("kcp-datagram-inside")

	shim := buildSpoofShim(SpoofProfileUDP, port, 0, src, dst, payload)

	// Ports and length are where we said.
	if binary.BigEndian.Uint16(shim[2:4]) != port {
		t.Fatalf("dest port not stamped")
	}
	if int(binary.BigEndian.Uint16(shim[4:6])) != len(shim) {
		t.Fatalf("udp length wrong")
	}
	verifyChecksum(t, src, dst, 17, shim)

	got, ok := stripSpoofShim(SpoofProfileUDP, port, shim)
	if !ok || !bytes.Equal(got, payload) {
		t.Fatalf("udp shim did not round-trip: ok=%v got=%q", ok, got)
	}
}

func TestSpoofTCPShimRoundTrip(t *testing.T) {
	src := net.ParseIP("81.28.60.1")
	dst := net.ParseIP("38.87.117.94")
	const port = 51234
	payload := bytes.Repeat([]byte{0xab}, 133) // odd length exercises the checksum tail

	shim := buildSpoofShim(SpoofProfileTCP, port, 12345, src, dst, payload)
	verifyChecksum(t, src, dst, 6, shim)

	got, ok := stripSpoofShim(SpoofProfileTCP, port, shim)
	if !ok || !bytes.Equal(got, payload) {
		t.Fatalf("tcp shim did not round-trip: ok=%v got len=%d", ok, len(got))
	}
}

// A packet for another tunnel's port must be rejected, so two tunnels on one
// host never read each other's traffic.
func TestSpoofShimRejectsWrongPort(t *testing.T) {
	src := net.ParseIP("10.0.0.1")
	dst := net.ParseIP("10.0.0.2")
	shim := buildSpoofShim(SpoofProfileUDP, 1111, 0, src, dst, []byte("x"))
	if _, ok := stripSpoofShim(SpoofProfileUDP, 2222, shim); ok {
		t.Fatal("a packet for a different port was accepted")
	}
}

// A truncated packet must be rejected rather than slicing out of bounds.
func TestSpoofShimRejectsShort(t *testing.T) {
	if _, ok := stripSpoofShim(SpoofProfileUDP, 1234, []byte{1, 2, 3}); ok {
		t.Fatal("a too-short udp packet was accepted")
	}
	if _, ok := stripSpoofShim(SpoofProfileTCP, 1234, make([]byte, 12)); ok {
		t.Fatal("a too-short tcp packet was accepted")
	}
}

func TestParseSpoofProfile(t *testing.T) {
	for _, in := range []string{"", "udp"} {
		if p, err := ParseSpoofProfile(in); err != nil || p != SpoofProfileUDP {
			t.Errorf("%q should be udp: %v %v", in, p, err)
		}
	}
	if p, err := ParseSpoofProfile("tcp"); err != nil || p != SpoofProfileTCP {
		t.Errorf("tcp should parse: %v %v", p, err)
	}
	if _, err := ParseSpoofProfile("gre"); err == nil {
		t.Error("an unsupported profile should be rejected")
	}
}

// The identity is deterministic per token and its port is kept out of the
// well-known range, so the two ends agree and the flow reads as ephemeral.
func TestSpoofIdentityDeterministic(t *testing.T) {
	tagA, portA := spoofIdentity("shared-token")
	tagB, portB := spoofIdentity("shared-token")
	if tagA != tagB || portA != portB {
		t.Fatal("identity is not deterministic for one token")
	}
	if _, portC := spoofIdentity("other-token"); portC == portA && tagA == tagB {
		// A different token should almost always differ; only flag an exact
		// collision in both, which is astronomically unlikely.
		if tagC, _ := spoofIdentity("other-token"); tagC == tagA {
			t.Fatal("two tokens collided in both tag and port")
		}
	}
	if portA < 1024 {
		t.Fatalf("port %d is in the well-known range", portA)
	}
}
