package network

import (
	"golang.org/x/net/bpf"
	"golang.org/x/net/icmp"
)

// Keeping other people's ICMP out of an xdi socket, in the kernel.
//
// A raw ICMP socket is handed a copy of every ICMP packet the host receives,
// and a client opens one per pooled session. With the default pool that is
// nine sockets, each reading, copying and parsing every echo reply meant for
// the other eight, and every ping and unreachable besides, before throwing it
// away in userspace. Measured under load: the client side of an xdi tunnel
// spent seven cores on a transfer the server side did with under two, most of
// it in that read loop.
//
// A socket filter makes the kernel do the matching: a client socket is given
// only echo replies carrying its own identifier, the server socket only echo
// requests. What the filter lets through is still checked in ReadFrom — the
// tag, the direction — so this changes the cost, never the answer; where the
// filter cannot be attached the socket simply works as it did.
//
// The filter sees the packet as the kernel holds it, IPv4 header included,
// so the ICMP header is found past the header's own length.
func icmpFilter(wantType uint8, id int) []bpf.Instruction {
	prog := []bpf.Instruction{
		bpf.LoadMemShift{Off: 0},          // X = IPv4 header length
		bpf.LoadIndirect{Off: 0, Size: 1}, // A = ICMP type
		bpf.JumpIf{Cond: bpf.JumpNotEqual, Val: uint32(wantType), SkipTrue: 3},
		bpf.LoadIndirect{Off: 4, Size: 2}, // A = echo identifier
		bpf.JumpIf{Cond: bpf.JumpNotEqual, Val: uint32(id), SkipTrue: 1},
		bpf.RetConstant{Val: 0xffff}, // accept
		bpf.RetConstant{Val: 0},      // drop
	}
	if id < 0 {
		// Any identifier: the server answers every client.
		prog = []bpf.Instruction{
			bpf.LoadMemShift{Off: 0},
			bpf.LoadIndirect{Off: 0, Size: 1},
			bpf.JumpIf{Cond: bpf.JumpNotEqual, Val: uint32(wantType), SkipTrue: 1},
			bpf.RetConstant{Val: 0xffff},
			bpf.RetConstant{Val: 0},
		}
	}
	return prog
}

// attachICMPFilter installs icmpFilter on a real raw socket. Best effort: a
// socket the filter cannot be put on keeps filtering in userspace.
func attachICMPFilter(pc icmpSocket, wantType uint8, id int) {
	real, ok := pc.(*icmp.PacketConn)
	if !ok {
		return
	}
	p4 := real.IPv4PacketConn()
	if p4 == nil {
		return
	}
	raw, err := bpf.Assemble(icmpFilter(wantType, id))
	if err != nil {
		return
	}
	_ = p4.SetBPF(raw)
}
