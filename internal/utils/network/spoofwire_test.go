package network

import (
	"encoding/binary"
	"net"
	"sync/atomic"
	"testing"
)

// The spoofed TCP segment must be a SYN.
//
// This is the divergence from the spoof-tunnel reference that could stop a
// tunnel outright, and it is why "IP spoofing does not work" came back from
// several people at once while the reference worked on the same paths.
//
// The carrier used to stamp PSH|ACK, meaning to look like traffic on an
// established connection. There is no established connection — no handshake
// ever happens — so to anything on the path that tracks state, every segment
// is out of state, and dropping out-of-state TCP is the first thing a stateful
// firewall does. The sockets stay healthy and nothing crosses, which is exactly
// what was reported.
//
// A SYN cannot be out of state: it is the packet that opens a connection.
func TestTheSpoofedTCPSegmentIsASYN(t *testing.T) {
	src, dst := net.IPv4(203, 0, 113, 7), net.IPv4(198, 51, 100, 9)
	seg := buildTCPShimPorts(4000, 4000, 1, src, dst, []byte("payload"))

	const syn = 0x02
	if got := seg[13]; got != syn {
		t.Fatalf("the flag byte is %#02x, want %#02x (SYN). %#02x is PSH|ACK, which "+
			"a stateful firewall drops as out of state because no handshake "+
			"preceded it", got, syn, got)
	}
	if ack := binary.BigEndian.Uint32(seg[8:12]); ack != 0 {
		t.Errorf("the acknowledgement field is %d; a SYN's is zero", ack)
	}
}

// Whatever the sender stamps, the receiver must not care — or a peer on an
// older build stops understanding this one. The strip reads the ports and the
// data offset and nothing else, which is what makes the flag change safe to
// deploy one end at a time.
func TestTheReceiverIgnoresTheTCPFlags(t *testing.T) {
	src, dst := net.IPv4(203, 0, 113, 7), net.IPv4(198, 51, 100, 9)
	body := []byte("carried")

	for _, flags := range []byte{0x02, 0x18, 0x10, 0x00} {
		seg := buildTCPShimPorts(4000, 4000, 1, src, dst, body)
		seg[13] = flags

		got, ok := stripSpoofShim(SpoofProfileTCP, 4000, seg)
		if !ok {
			t.Fatalf("a segment with flags %#02x was rejected", flags)
		}
		if string(got) != string(body) {
			t.Fatalf("flags %#02x: recovered %q, want %q", flags, got, body)
		}
	}
}

// The sequence space counts the bytes actually sent. Advancing by the length
// plus one counts a byte that was never on the wire and leaves a hole in the
// space on every segment, which anything following the flow can see.
func TestTheSequenceSpaceCountsOnlyWhatWasSent(t *testing.T) {
	var counter atomic.Uint32

	first := nextTCPSeq(&counter, 100)
	if first != 1 {
		t.Fatalf("the first segment starts at %d, want 1", first)
	}
	second := nextTCPSeq(&counter, 50)
	if second != first+100 {
		t.Fatalf("after a 100-byte segment the next sequence is %d, want %d — the "+
			"space has a hole in it", second, first+100)
	}
	if third := nextTCPSeq(&counter, 0); third != second+50 {
		t.Fatalf("after a 50-byte segment the next sequence is %d, want %d", third, second+50)
	}
}

// The identification field must not tie the forged sources back together.
//
// A counter running 1, 2, 3 across packets that each claim a different source
// address is one flow wearing a dozen hats, and the counter says so. That
// defeats the point of rotating the source at all.
func TestTheIPIdentifierIsNotACounter(t *testing.T) {
	const draws = 64

	seen := make(map[uint16]int, draws)
	consecutive := 0
	prev := randomIPID()
	seen[prev]++
	for i := 1; i < draws; i++ {
		id := randomIPID()
		seen[id]++
		if id == prev+1 {
			consecutive++
		}
		prev = id
	}

	// A counter would produce draws-1 consecutive pairs and draws distinct
	// values in a straight line. Random draws produce neither.
	if consecutive > draws/4 {
		t.Fatalf("%d of %d identifiers followed the previous one by exactly 1 — "+
			"this is a counter, not a random draw", consecutive, draws-1)
	}
	if len(seen) < draws/2 {
		t.Errorf("only %d distinct identifiers in %d draws", len(seen), draws)
	}
}

// The UDP checksum is ours to keep. The reference sends zero, which IPv4 allows
// and which is never wrong; a correct checksum is never wrong either, and is
// accepted everywhere a zero one is. What would be wrong is computing it over a
// pseudo-header that does not match the address the packet is sent with — the
// receiver's kernel validates against the forged source it sees, so that is the
// one that has to go into the sum.
func TestTheUDPChecksumCoversTheForgedSource(t *testing.T) {
	forged, dst := net.IPv4(203, 0, 113, 7), net.IPv4(198, 51, 100, 9)
	shim := buildUDPShimPorts(4000, 4000, forged, dst, []byte("payload"))

	if got := l4Checksum(forged, dst, 17, shim); got != 0 {
		t.Fatalf("the checksum does not verify against the forged source (%#04x); "+
			"every packet would be dropped by the receiving kernel", got)
	}

	// And it must not verify against some other source, which is the mistake
	// this guards: computing over the real local address while the header
	// carries the forged one.
	other := net.IPv4(192, 0, 2, 1)
	if got := l4Checksum(other, dst, 17, shim); got == 0 {
		t.Error("the checksum verifies against an address the packet was not sent " +
			"with, so it cannot be covering the source at all")
	}
}
