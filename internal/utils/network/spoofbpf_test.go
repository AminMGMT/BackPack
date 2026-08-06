package network

import (
	"testing"

	"golang.org/x/net/bpf"
)

// The filters must assemble, and — run through the reference BPF VM against
// hand-built IPv4 packets — must accept exactly this tunnel's flow and drop
// everything else. This is the whole guarantee: a wrong offset would silently
// drop the tunnel's own traffic, which no build check would catch.

func runBPF(t *testing.T, profile SpoofProfile, port uint16, packet []byte) bool {
	t.Helper()
	raw, err := spoofBPFProgram(profile, port)
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
	if !runBPF(t, SpoofProfileTCP, port, ip4pkt(6, tcp(port))) {
		t.Error("tcp segment on the tunnel port must be accepted")
	}
	if runBPF(t, SpoofProfileTCP, port, ip4pkt(6, tcp(port+1))) {
		t.Error("tcp segment on another port must be dropped")
	}
}

func TestSpoofBPFICMP(t *testing.T) {
	const id = 0x4321
	echo := func(typ byte, ident uint16) []byte {
		b := make([]byte, 8)
		b[0] = typ
		b[4] = byte(ident >> 8)
		b[5] = byte(ident)
		return b
	}
	if !runBPF(t, SpoofProfileICMP, id, ip4pkt(1, echo(icmpTypeEchoRequest, id))) {
		t.Error("echo request with our id must be accepted")
	}
	if !runBPF(t, SpoofProfileICMP, id, ip4pkt(1, echo(icmpTypeEchoReply, id))) {
		t.Error("echo reply with our id must be accepted")
	}
	if runBPF(t, SpoofProfileICMP, id, ip4pkt(1, echo(icmpTypeEchoRequest, id+1))) {
		t.Error("echo with another id must be dropped")
	}
	if runBPF(t, SpoofProfileICMP, id, ip4pkt(1, echo(3, id))) { // destination unreachable
		t.Error("a non-echo icmp message must be dropped")
	}
}
