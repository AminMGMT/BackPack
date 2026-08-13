package network

import (
	"testing"

	"golang.org/x/net/bpf"
)

// The filters must assemble, and — run through the reference BPF VM against
// hand-built IPv4 packets — must accept exactly this tunnel's flow and drop
// everything else. This is the whole guarantee: a wrong offset would silently
// drop the tunnel's own traffic, which no build check would catch.

func runBPF(t *testing.T, profile SpoofProfile, port uint16, wantType byte, packet []byte) bool {
	t.Helper()
	raw, err := spoofBPFProgram(profile, port, wantType)
	if err != nil {
		t.Fatalf("assemble %s: %v", profile, err)
	}
	vm, err := bpf.NewVM(rawToInstr(t, raw))
	if err != nil {
		t.Fatalf("vm: %v", err)
	}
	out, err := vm.Run(packet)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return out > 0
}

// rawToInstr disassembles so the VM (which wants []bpf.Instruction) can run the
// same bytes the kernel would.
func rawToInstr(t *testing.T, raw []bpf.RawInstruction) []bpf.Instruction {
	t.Helper()
	ins := make([]bpf.Instruction, len(raw))
	for i, r := range raw {
		ins[i] = r.Disassemble()
	}
	return ins
}

// ip4pkt builds a minimal 20-byte IPv4 header (IHL 5) in front of an L4 payload.
func ip4pkt(proto byte, l4 []byte) []byte {
	h := make([]byte, 20)
	h[0] = 0x45 // version 4, IHL 5
	h[9] = proto
	return append(h, l4...)
}

func TestSpoofBPFTCP(t *testing.T) {
	const port = 51234
	tcp := func(dstPort uint16) []byte {
		b := make([]byte, 20)
		b[2] = byte(dstPort >> 8)
		b[3] = byte(dstPort)
		return b
	}
	if !runBPF(t, SpoofProfileTCP, port, 0, ip4pkt(6, tcp(port))) {
		t.Error("tcp segment on the tunnel port must be accepted")
	}
	if runBPF(t, SpoofProfileTCP, port, 0, ip4pkt(6, tcp(port+1))) {
		t.Error("tcp segment on another port must be dropped")
	}
}

func echoMsg(typ byte, ident uint16) []byte {
	b := make([]byte, 8)
	b[0] = typ
	b[4] = byte(ident >> 8)
	b[5] = byte(ident)
	return b
}

func TestSpoofBPFICMP(t *testing.T) {
	const id = 0x4321
	// Default: both ends send Echo Requests (type 8), so wantType is 8.
	if !runBPF(t, SpoofProfileICMP, id, icmpTypeEchoRequest, ip4pkt(1, echoMsg(icmpTypeEchoRequest, id))) {
		t.Error("echo request with our id must be accepted")
	}
	// A reply is then the kernel's own and must be dropped in the kernel.
	if runBPF(t, SpoofProfileICMP, id, icmpTypeEchoRequest, ip4pkt(1, echoMsg(icmpTypeEchoReply, id))) {
		t.Error("echo reply must be dropped; only requests are the tunnel's")
	}
	if runBPF(t, SpoofProfileICMP, id, icmpTypeEchoRequest, ip4pkt(1, echoMsg(icmpTypeEchoRequest, id+1))) {
		t.Error("echo with another id must be dropped")
	}
	if runBPF(t, SpoofProfileICMP, id, icmpTypeEchoRequest, ip4pkt(1, echoMsg(3, id))) { // destination unreachable
		t.Error("a non-echo icmp message must be dropped")
	}
}

func TestSpoofBPFICMPReplySplit(t *testing.T) {
	const id = 0x4321
	// With the reply split on, the client accepts the server's Echo Reply (type
	// 0) and must reject a request — the mirror of the default.
	if !runBPF(t, SpoofProfileICMP, id, icmpTypeEchoReply, ip4pkt(1, echoMsg(icmpTypeEchoReply, id))) {
		t.Error("client with reply-split must accept the server's echo reply")
	}
	if runBPF(t, SpoofProfileICMP, id, icmpTypeEchoReply, ip4pkt(1, echoMsg(icmpTypeEchoRequest, id))) {
		t.Error("client with reply-split must drop echo requests")
	}
}

func TestSpoofBPFICMPv6(t *testing.T) {
	const id = 0x4321
	// icmpv6 rides proto 58 in IPv4 with echo type 128; the filter accepts it and
	// rejects the ICMPv4 request type on the same socket.
	if !runBPF(t, SpoofProfileICMPv6, id, icmpv6TypeEchoRequest, ip4pkt(58, echoMsg(icmpv6TypeEchoRequest, id))) {
		t.Error("icmpv6 echo request (type 128) with our id must be accepted")
	}
	if runBPF(t, SpoofProfileICMPv6, id, icmpv6TypeEchoRequest, ip4pkt(58, echoMsg(icmpTypeEchoRequest, id))) {
		t.Error("an icmpv4 request type must be dropped by the icmpv6 filter")
	}
}
