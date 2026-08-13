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

func TestSpoofICMPEchoRoundTrip(t *testing.T) {
	const id = 0x4321
	payload := []byte("kcp-over-ping")

	// Both ends send an Echo Request (type 8); it carries the identifier and
	// must parse back to the bare payload — no tag or direction prefix.
	msg := buildICMPEcho(icmpTypeEchoRequest, id, 7, payload)
	// The ICMP checksum spans the whole message and must verify to zero.
	if got := onesComplement(msg); got != 0 {
		t.Fatalf("icmp checksum does not verify to zero: %#04x", got)
	}
	got, ok := parseICMPEcho(icmpTypeEchoRequest, id, msg)
	if !ok || !bytes.Equal(got, payload) {
		t.Fatalf("icmp echo did not round-trip: ok=%v", ok)
	}

	// A wrong identifier (another tunnel's) is rejected.
	if _, ok := parseICMPEcho(icmpTypeEchoRequest, id+1, buildICMPEcho(icmpTypeEchoRequest, id, 1, payload)); ok {
		t.Fatal("an echo with a different identifier was accepted")
	}
	// An Echo Reply (type 0) — the kernel's automatic answer to an inbound
	// request — is rejected: only Echo Requests are the tunnel's, which is what
	// lets the direction byte go.
	if _, ok := parseICMPEcho(icmpTypeEchoRequest, id, buildICMPEcho(icmpTypeEchoReply, id, 1, payload)); ok {
		t.Fatal("an echo reply was accepted; the kernel's own replies would be read as tunnel data")
	}
	// A non-echo ICMP type is rejected.
	bad := buildICMPEcho(icmpTypeEchoRequest, id, 1, payload)
	bad[0] = 3 // destination unreachable
	if _, ok := parseICMPEcho(icmpTypeEchoRequest, id, bad); ok {
		t.Fatal("a non-echo ICMP message was accepted")
	}
	// A truncated message is rejected, not sliced out of bounds.
	if _, ok := parseICMPEcho(icmpTypeEchoRequest, id, []byte{8, 0, 0}); ok {
		t.Fatal("a too-short icmp message was accepted")
	}
}

func TestParseSpoofPool(t *testing.T) {
	// No source configured spoofs nothing.
	if ips, err := parseSpoofPool("", nil); err != nil || len(ips) != 0 {
		t.Fatalf("empty should be none: %v %v", ips, err)
	}
	// A single address becomes a one-element pool.
	if ips, err := parseSpoofPool("81.28.60.1", nil); err != nil || len(ips) != 1 || ips[0].String() != "81.28.60.1" {
		t.Fatalf("single: %v %v", ips, err)
	}
	// The whole pool is parsed and kept in order.
	pool := []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"}
	ips, err := parseSpoofPool("", pool)
	if err != nil || len(ips) != 3 || ips[0].String() != "1.1.1.1" || ips[2].String() != "9.9.9.9" {
		t.Fatalf("pool: %v %v", ips, err)
	}
	// A bad entry is rejected, not silently dropped.
	if _, err := parseSpoofPool("not-an-ip", nil); err == nil {
		t.Fatal("an invalid address should be an error")
	}
}

func TestParseSpoofProfile(t *testing.T) {
	for _, in := range []string{"", "udp"} {
		if p, err := ParseSpoofProfile(in); err != nil || p != SpoofProfileUDP {
			t.Errorf("%q should be udp: %v %v", in, p, err)
		}
	}
	for in, want := range map[string]SpoofProfile{
		"tcp":    SpoofProfileTCP,
		"icmp":   SpoofProfileICMP,
		"icmpv6": SpoofProfileICMPv6,
		"ipip":   SpoofProfileIPIP,
		"gre":    SpoofProfileGRE,
	} {
		if p, err := ParseSpoofProfile(in); err != nil || p != want {
			t.Errorf("%q should parse to %q: %v %v", in, want, p, err)
		}
	}
	if _, err := ParseSpoofProfile("carrier-pigeon"); err == nil {
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

// Fragmentation must tile the whole segment with no gap or overlap, keep every
// fragment (but the last) a multiple of 8 bytes and inside the MTU, and flag
// more-fragments on all but the last. A wrong offset here corrupts the peer's
// reassembly silently, so the arithmetic is checked directly.
func TestSpoofFragments(t *testing.T) {
	const mtu = 1500
	// A segment that fits comes back whole, not fragmented.
	if fr := spoofFragments(1000, mtu); len(fr) != 1 || fr[0] != (spoofFragRange{0, 1000, false}) {
		t.Fatalf("a segment under the MTU must not fragment: %+v", fr)
	}
	// An oversize segment fragments; verify the invariants.
	const segLen = 4000
	frags := spoofFragments(segLen, mtu)
	if len(frags) < 2 {
		t.Fatalf("a %d-byte segment over MTU %d must fragment: got %d", segLen, mtu, len(frags))
	}
	want := 0
	for i, f := range frags {
		if f.off != want {
			t.Fatalf("fragment %d starts at %d, expected %d (gap/overlap)", i, f.off, want)
		}
		if 20+(f.end-f.off) > mtu {
			t.Fatalf("fragment %d of %d bytes exceeds MTU %d", i, f.end-f.off, mtu)
		}
		last := i == len(frags)-1
		if !last {
			if (f.end-f.off)%8 != 0 {
				t.Fatalf("non-final fragment %d is not a multiple of 8: %d", i, f.end-f.off)
			}
			if !f.more {
				t.Fatalf("non-final fragment %d must set more-fragments", i)
			}
		} else if f.more {
			t.Fatalf("the final fragment must not set more-fragments")
		}
		want = f.end
	}
	if want != segLen {
		t.Fatalf("fragments cover %d bytes, expected the whole %d", want, segLen)
	}
}

// The obfuscation transforms must round-trip: whatever WriteTo layers on, the
// receiver's undo must recover the exact original bytes, or the tunnel corrupts
// silently. Each is checked directly here, without a socket.
func TestSpoofPaddingRoundTrip(t *testing.T) {
	orig := []byte("wireguard-datagram-payload")
	for i := 0; i < 200; i++ {
		padded := applyPadding(orig, 64)
		if len(padded) <= len(orig) {
			t.Fatalf("padding did not grow the payload: %d <= %d", len(padded), len(orig))
		}
		got, ok := stripPadding(padded)
		if !ok || !bytes.Equal(got, orig) {
			t.Fatalf("padding did not round-trip: ok=%v got=%q", ok, got)
		}
	}
	// A byte claiming more padding than exists is rejected, not mis-sliced.
	if _, ok := stripPadding([]byte{0xff}); ok {
		t.Fatal("an impossible pad length was accepted")
	}
	if _, ok := stripPadding(nil); ok {
		t.Fatal("empty input was accepted")
	}
}

func TestSpoofFakeTLSRoundTrip(t *testing.T) {
	orig := []byte("payload behind a fake TLS record")
	wrapped := applyFakeTLS(orig)
	if wrapped[0] != 0x17 || wrapped[1] != 0x03 || wrapped[2] != 0x03 {
		t.Fatalf("fake TLS header is not a TLS 1.2 application_data record: % x", wrapped[:3])
	}
	got, ok := stripFakeTLS(wrapped)
	if !ok || !bytes.Equal(got, orig) {
		t.Fatalf("fake TLS did not round-trip: ok=%v", ok)
	}
	// A segment that does not start with the record header is rejected.
	if _, ok := stripFakeTLS([]byte{0x16, 0x03, 0x03, 0, 0, 1}); ok {
		t.Fatal("a non-application-data record was accepted")
	}
}

func TestSpoofGRERoundTrip(t *testing.T) {
	orig := []byte("gre-encapsulated payload")
	shim := buildGREShim(orig)
	if len(shim) != greHeaderLen+len(orig) {
		t.Fatalf("gre shim wrong length: %d", len(shim))
	}
	got, ok := stripGREShim(shim)
	if !ok || !bytes.Equal(got, orig) {
		t.Fatalf("gre did not round-trip: ok=%v", ok)
	}
	if _, ok := stripGREShim([]byte{0, 0}); ok {
		t.Fatal("a too-short gre packet was accepted")
	}
}

func TestSpoofSrcPortShuffle(t *testing.T) {
	// Off: always the fixed port.
	off := SpoofDPI{}
	for i := 0; i < 20; i++ {
		if p := off.pickSrcPort(4444); p != 4444 {
			t.Fatalf("shuffle off must return the fixed port, got %d", p)
		}
	}
	// On: always inside the range.
	on := SpoofDPI{ShufflePort: true, PortMin: 50000, PortMax: 50010}
	seen := map[uint16]bool{}
	for i := 0; i < 500; i++ {
		p := on.pickSrcPort(4444)
		if p < 50000 || p > 50010 {
			t.Fatalf("shuffled port %d out of range", p)
		}
		seen[p] = true
	}
	if len(seen) < 2 {
		t.Fatal("shuffle produced no variation")
	}
}
