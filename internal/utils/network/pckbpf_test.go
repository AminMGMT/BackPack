package network

import (
	"encoding/binary"
	"net"
	"testing"

	"golang.org/x/net/bpf"
	"golang.org/x/net/ipv4"
)

// The receive filter runs in the kernel on every packet the machine sees, and a
// mistake in it fails in one of two silent ways: too tight and the tunnel never
// receives anything while every other sign of health is green, too loose and the
// read loop wakes on the whole machine's traffic. Neither shows up as an error.
//
// x/net/bpf ships a VM for exactly this program format, so the filter is run
// here against real frames rather than eyeballed.

func runFilter(t *testing.T, port uint16, linkLen int, frame []byte) bool {
	t.Helper()
	raw, err := pckBPFProgram(port, linkLen)
	if err != nil {
		t.Fatalf("assembling the filter for link length %d failed: %v", linkLen, err)
	}
	// Round-trip through the raw form the kernel is given, so what is tested is
	// what would be installed.
	insns, ok := bpf.Disassemble(raw)
	if !ok {
		t.Fatalf("the assembled filter does not disassemble cleanly")
	}
	vm, err := bpf.NewVM(insns)
	if err != nil {
		t.Fatalf("the filter is not a valid BPF program: %v", err)
	}
	n, err := vm.Run(frame)
	if err != nil {
		t.Fatalf("running the filter failed: %v", err)
	}
	return n > 0
}

// framePair builds the same packet with and without a link header, so every
// case can be asserted on both kinds of interface.
func framePair(dstPort uint16, payload []byte) (raw, eth []byte) {
	tcp := buildPckTCP(40000, dstPort, 1, 2, FlagPSH|FlagACK, 10, 20, testSrc, testDst, payload)
	raw = buildPckIPv4(1, testSrc, testDst, tcp)
	eth = buildPckEthernet(net.HardwareAddr{1, 2, 3, 4, 5, 6}, net.HardwareAddr{6, 5, 4, 3, 2, 1}, raw)
	return raw, eth
}

// The tunnel's own packets must get through, on both kinds of link.
func TestFilterAcceptsTheTunnelsPackets(t *testing.T) {
	raw, eth := framePair(443, []byte("payload"))
	if !runFilter(t, 443, 0, raw) {
		t.Fatal("the filter rejected the tunnel's own packet on a link with no L2 header")
	}
	if !runFilter(t, 443, pckEthHeaderLen, eth) {
		t.Fatal("the filter rejected the tunnel's own packet on Ethernet")
	}
}

// Everything else on the wire must not.
func TestFilterRejectsEverythingElse(t *testing.T) {
	rawOther, ethOther := framePair(8080, []byte("someone else's"))
	if runFilter(t, 443, 0, rawOther) {
		t.Fatal("a segment for another port passed the filter")
	}
	if runFilter(t, 443, pckEthHeaderLen, ethOther) {
		t.Fatal("a segment for another port passed the filter on Ethernet")
	}

	raw, eth := framePair(443, []byte("payload"))

	// UDP on the same port, which a busy machine will have.
	udp := append([]byte(nil), raw...)
	udp[9] = 17
	binary.BigEndian.PutUint16(udp[10:12], 0)
	binary.BigEndian.PutUint16(udp[10:12], onesComplement(udp[:ipv4.HeaderLen]))
	if runFilter(t, 443, 0, udp) {
		t.Fatal("a UDP packet passed the filter")
	}

	// A fragment, whose TCP header may be absent entirely — the case that would
	// have the parser reading a port out of somebody else's payload.
	frag := append([]byte(nil), raw...)
	binary.BigEndian.PutUint16(frag[6:8], 0x0025)
	if runFilter(t, 443, 0, frag) {
		t.Fatal("a fragment passed the filter")
	}

	// A non-IPv4 ethertype on an Ethernet link — ARP, say, which is constant.
	arp := append([]byte(nil), eth...)
	binary.BigEndian.PutUint16(arp[12:14], 0x0806)
	if runFilter(t, 443, pckEthHeaderLen, arp) {
		t.Fatal("a non-IPv4 frame passed the filter")
	}

	// IPv6 on a link with no L2 header, where the version nibble is the only
	// thing distinguishing it.
	v6 := append([]byte(nil), raw...)
	v6[0] = 0x60
	if runFilter(t, 443, 0, v6) {
		t.Fatal("an IPv6 packet passed the filter")
	}
}

// A packet whose IP header carries options shifts the TCP header along, and the
// filter has to follow it there rather than read a fixed offset.
func TestFilterFollowsIPOptions(t *testing.T) {
	tcp := buildPckTCP(40000, 443, 1, 2, FlagPSH|FlagACK, 10, 20, testSrc, testDst, []byte("x"))

	const optLen = 4
	withOpts := make([]byte, ipv4.HeaderLen+optLen+len(tcp))
	base := buildPckIPv4(1, testSrc, testDst, tcp)
	copy(withOpts, base[:ipv4.HeaderLen])
	withOpts[0] = 4<<4 | byte((ipv4.HeaderLen+optLen)/4)
	binary.BigEndian.PutUint16(withOpts[2:4], uint16(len(withOpts)))
	// A no-op option, so the header is longer without meaning anything.
	for i := 0; i < optLen; i++ {
		withOpts[ipv4.HeaderLen+i] = 1
	}
	copy(withOpts[ipv4.HeaderLen+optLen:], tcp)
	binary.BigEndian.PutUint16(withOpts[10:12], 0)
	binary.BigEndian.PutUint16(withOpts[10:12], onesComplement(withOpts[:ipv4.HeaderLen+optLen]))

	if !runFilter(t, 443, 0, withOpts) {
		t.Fatal("a packet with IP options was rejected — the filter is reading a fixed offset")
	}
	// And the parser has to agree with the filter about where the header ended.
	if _, ok := parsePckFrame(withOpts, 0, 443); !ok {
		t.Fatal("the parser rejected a packet with IP options that the filter accepted")
	}
}

// A short or truncated frame must not make the filter misbehave, and whatever
// it does let through must be caught by the parser behind it.
//
// The filter is deliberately not the boundary here. It cannot cheaply check
// that a frame is long enough to hold everything the parser will read — a
// 24-byte frame has a destination port to compare and nothing else — so it
// passes some of them, and the parser is what declines them. This asserts that
// division of labour rather than a filter that does not have the information to
// be exact.
func TestFilterAndParserTogetherRejectShortFrames(t *testing.T) {
	raw, _ := framePair(443, []byte("payload"))
	prog, err := pckBPFProgram(443, 0)
	if err != nil {
		t.Fatalf("assembling the filter failed: %v", err)
	}
	insns, ok := bpf.Disassemble(prog)
	if !ok {
		t.Fatal("the assembled filter does not disassemble cleanly")
	}
	vm, err := bpf.NewVM(insns)
	if err != nil {
		t.Fatalf("the filter is not valid: %v", err)
	}

	for i := 0; i < len(raw); i++ {
		n, err := vm.Run(raw[:i])
		if err != nil {
			t.Fatalf("a %d-byte frame made the filter error: %v", i, err)
		}
		if n == 0 {
			continue // the filter already declined it
		}
		if _, ok := parsePckFrame(raw[:i], 0, 443); ok && i < len(raw) {
			t.Fatalf("a %d-byte frame passed both the filter and the parser, "+
				"but the whole packet is %d bytes", i, len(raw))
		}
	}
}
