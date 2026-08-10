package network

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"

	"golang.org/x/net/ipv4"
)

// What this transport is for is looking, to anything inspecting the wire, like
// an ordinary established TCP connection. Everything below asserts one of the
// ways it could stop doing that — and every one of them is a difference the
// tunnel would go on working through, carrying traffic perfectly while being
// trivially classifiable. That is the failure this file exists to catch, since
// nothing at runtime ever will.

var (
	testSrc = net.IPv4(203, 0, 113, 10).To4()
	testDst = net.IPv4(198, 51, 100, 20).To4()
)

func buildTestFrame(t *testing.T, srcPort, dstPort uint16, seq, ack uint32, payload []byte) []byte {
	t.Helper()
	tcp := buildPckTCP(srcPort, dstPort, seq, ack, FlagPSH|FlagACK, 111111, 222222, testSrc, testDst, payload)
	return buildPckIPv4(1234, testSrc, testDst, tcp)
}

// A segment must carry the timestamp option. Linux puts one on every segment of
// every connection; one without is not a Linux flow, and the absence is visible
// in the header length alone without looking at a single byte of payload.
func TestSegmentCarriesTimestamps(t *testing.T) {
	ip := buildTestFrame(t, 40000, 443, 100, 200, []byte("hello"))
	tcp := ip[ipv4.HeaderLen:]

	off := int(tcp[12]>>4) * 4
	if off != pckTCPHeaderLen+pckTCPOptionsLen {
		t.Fatalf("data offset is %d bytes, want %d — the options are missing",
			off, pckTCPHeaderLen+pckTCPOptionsLen)
	}
	if got := tcpTimestamp(tcp[pckTCPHeaderLen:off]); got != 111111 {
		t.Fatalf("timestamp option reads %d, want 111111", got)
	}
}

// The acknowledgement number on an ACK segment must be the peer's data, not
// zero. A segment with the ACK flag set and an acknowledgement of zero is a
// contradiction no stack produces.
func TestAckIsNotZeroOnAnAckSegment(t *testing.T) {
	ip := buildTestFrame(t, 40000, 443, 100, 4242, nil)
	tcp := ip[ipv4.HeaderLen:]
	if tcp[13]&byte(FlagACK) == 0 {
		t.Fatal("the test segment does not have ACK set")
	}
	if got := binary.BigEndian.Uint32(tcp[8:12]); got != 4242 {
		t.Fatalf("ack is %d, want 4242", got)
	}
}

// Source and destination port must be able to differ, and the ports written
// must be the ones asked for. A flow whose two ports are equal is close to
// nonexistent in real traffic and is a single-field giveaway.
func TestPortsAreDistinctAndAsGiven(t *testing.T) {
	ip := buildTestFrame(t, 40000, 443, 1, 1, nil)
	tcp := ip[ipv4.HeaderLen:]
	src := binary.BigEndian.Uint16(tcp[0:2])
	dst := binary.BigEndian.Uint16(tcp[2:4])
	if src != 40000 || dst != 443 {
		t.Fatalf("ports are %d->%d, want 40000->443", src, dst)
	}
	if src == dst {
		t.Fatal("source and destination port are equal, which real traffic almost never is")
	}
}

// The checksums have to be right or nothing arrives at all — but a wrong one
// fails in a way that looks like a network problem, so it is worth pinning.
func TestChecksumsVerify(t *testing.T) {
	payload := []byte("some tunnel bytes")
	ip := buildTestFrame(t, 40000, 443, 7, 9, payload)

	if got := onesComplement(ip[:ipv4.HeaderLen]); got != 0 {
		t.Fatalf("IPv4 header checksum does not verify (residual %#04x)", got)
	}
	tcp := ip[ipv4.HeaderLen:]
	if got := l4Checksum(testSrc, testDst, 6, tcp); got != 0 {
		t.Fatalf("TCP checksum does not verify (residual %#04x)", got)
	}
}

// The IP header must carry the DSCP marking and the don't-fragment bit, both of
// which an ordinary Linux socket sets and neither of which anything else on this
// path would.
func TestIPHeaderLooksOrdinary(t *testing.T) {
	ip := buildTestFrame(t, 40000, 443, 1, 1, nil)
	if dscp := ip[1] >> 2; dscp != 46 {
		t.Fatalf("DSCP is %d, want 46 (EF)", dscp)
	}
	if flags := binary.BigEndian.Uint16(ip[6:8]); flags&0x4000 == 0 {
		t.Fatal("the don't-fragment bit is not set")
	}
	if ip[8] != 64 {
		t.Fatalf("TTL is %d, want 64", ip[8])
	}
}

// A frame built here must parse back to exactly what went in, over Ethernet and
// over a link with no L2 header at all.
func TestFrameRoundTrip(t *testing.T) {
	payload := []byte("the tunnel's datagram")
	ip := buildTestFrame(t, 40000, 443, 5000, 6000, payload)

	for _, tc := range []struct {
		name  string
		frame []byte
		link  int
	}{
		{"raw IP", ip, 0},
		{"ethernet", buildPckEthernet(
			net.HardwareAddr{1, 2, 3, 4, 5, 6},
			net.HardwareAddr{6, 5, 4, 3, 2, 1}, ip), pckEthHeaderLen},
	} {
		seg, ok := parsePckFrame(tc.frame, tc.link, 443)
		if !ok {
			t.Fatalf("%s: a frame this package built did not parse", tc.name)
		}
		if !bytes.Equal(seg.Payload, payload) {
			t.Fatalf("%s: payload came back as %q", tc.name, seg.Payload)
		}
		if seg.SrcPort != 40000 || seg.DstPort != 443 {
			t.Fatalf("%s: ports came back as %d->%d", tc.name, seg.SrcPort, seg.DstPort)
		}
		if seg.Seq != 5000 {
			t.Fatalf("%s: sequence came back as %d", tc.name, seg.Seq)
		}
		if !seg.SrcIP.Equal(testSrc) {
			t.Fatalf("%s: source came back as %s", tc.name, seg.SrcIP)
		}
		if seg.TSVal != 111111 {
			t.Fatalf("%s: timestamp came back as %d", tc.name, seg.TSVal)
		}
	}
}

// Anything not addressed to this tunnel's port must be rejected. The carrier
// reads a packet socket, which sees every packet on the machine, so this is the
// difference between a tunnel and a machine talking to itself.
func TestForeignTrafficIsRejected(t *testing.T) {
	ip := buildTestFrame(t, 40000, 8080, 1, 1, []byte("not ours"))
	if _, ok := parsePckFrame(ip, 0, 443); ok {
		t.Fatal("a segment for another port was accepted")
	}
}

// A frame that is truncated, malformed or simply something else must be
// declined rather than read past the end of. This reads straight off the wire,
// where a frame may be anything at all.
func TestMalformedFramesAreDeclined(t *testing.T) {
	full := buildTestFrame(t, 40000, 443, 1, 1, []byte("payload"))

	cases := map[string][]byte{
		"empty":             {},
		"one byte":          {0x45},
		"IPv4 header only":  full[:ipv4.HeaderLen],
		"half a TCP header": full[:ipv4.HeaderLen+10],
	}
	for name, frame := range cases {
		if _, ok := parsePckFrame(frame, 0, 443); ok {
			t.Fatalf("%s was accepted as a valid frame", name)
		}
	}

	// Every truncation of a real frame, as a lost fragment of one would be.
	for i := 0; i < len(full); i++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("parsing a frame truncated to %d bytes panicked: %v", i, r)
				}
			}()
			parsePckFrame(full[:i], 0, 443)
		}()
	}

	// A UDP packet, and a fragment, both of which the filter may let through.
	notTCP := append([]byte(nil), full...)
	notTCP[9] = 17
	if _, ok := parsePckFrame(notTCP, 0, 443); ok {
		t.Fatal("a UDP packet was accepted")
	}
	fragment := append([]byte(nil), full...)
	binary.BigEndian.PutUint16(fragment[6:8], 0x0001)
	if _, ok := parsePckFrame(fragment, 0, 443); ok {
		t.Fatal("a fragment was accepted — its TCP header may not even be present")
	}
}

// The flag spelling is the one an operator copies out of tcpdump, so it has to
// round-trip and it has to refuse what it does not understand.
func TestFlagParsing(t *testing.T) {
	for _, s := range []string{"PA", "A", "SA", "FA", "PAU"} {
		f, err := ParseTCPFlags(s)
		if err != nil {
			t.Fatalf("%q was refused: %v", s, err)
		}
		if got := f.String(); got != sortFlagString(s) {
			t.Fatalf("%q rendered back as %q", s, got)
		}
	}
	for _, s := range []string{"", "X", "PZ", "pa!"} {
		if _, err := ParseTCPFlags(s); err == nil {
			t.Fatalf("%q was accepted as a flag combination", s)
		}
	}
}

// sortFlagString puts the letters in the order String emits them, so the
// round-trip comparison is about content rather than ordering.
func sortFlagString(s string) string {
	var out []rune
	for _, fl := range flagLetters {
		for _, c := range s {
			if c == fl.c {
				out = append(out, fl.c)
				break
			}
		}
	}
	return string(out)
}

// A flag cycle that no stack would send is refused at config time rather than
// producing a flow that every middlebox drops.
func TestImplausibleFlagCyclesAreRefused(t *testing.T) {
	for _, bad := range [][]string{
		{"R"},       // a flow of resets
		{"PA", "R"}, // one reset in the middle tears it down
		{"P"},       // data with no ACK
		{"F"},       // a stream of FINs
	} {
		if _, err := ParseTCPFlagList(bad); err == nil {
			t.Fatalf("%v was accepted as a flag cycle", bad)
		}
	}
	// And the default, which must be what data actually carries.
	got, err := ParseTCPFlagList(nil)
	if err != nil {
		t.Fatalf("the default cycle was refused: %v", err)
	}
	if len(got) != 1 || got[0] != FlagPSH|FlagACK {
		t.Fatalf("the default cycle is %v, want a single PSH+ACK", FormatTCPFlagList(got))
	}
}

// KCP is sized against this figure. Too small and every datagram fragments —
// which on a path that drops fragments means the tunnel connects and carries
// nothing.
func TestOverheadMatchesWhatIsActuallyBuilt(t *testing.T) {
	ip := buildTestFrame(t, 40000, 443, 1, 1, nil)
	if len(ip) != pckOverhead {
		t.Fatalf("an empty packet is %d bytes but the overhead is declared as %d",
			len(ip), pckOverhead)
	}
	if PckOverhead() != pckOverhead {
		t.Fatalf("PckOverhead() reports %d, not %d", PckOverhead(), pckOverhead)
	}
}
