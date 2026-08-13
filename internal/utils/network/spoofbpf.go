package network

import (
	"fmt"

	"golang.org/x/net/bpf"
)

// The classic BPF program the tcp/icmp spoof carrier attaches to its raw receive
// socket. A raw IPv4 socket is handed every packet of its protocol the host
// sees; without a filter the read loop wakes on all of them and drops the ones
// that are not this tunnel's in userspace. This program does that filtering in
// the kernel instead, so only the tunnel's own flow is delivered — the same
// packets, far fewer wakeups on a busy server.
//
// It is an optimisation, never a correctness boundary: the frame's tag and
// direction are still checked after the read, so a tunnel whose kernel refuses
// the filter is slower, not wrong. Kept out of the Linux-only attach file so the
// program can be assembled and checked on any platform.
//
// The packet the filter sees starts at the IPv4 header, so the L4 header is at
// 4*(IHL) — loaded with the "MemShift" trick into X — and fields are read
// relative to it.

const bpfAccept = 262144 // snap length for an accepted packet; larger than any datagram

// spoofBPFProgram assembles the filter for a receive profile: tcp keeps segments
// whose destination port is the tunnel's; icmp/icmpv6 keep echo messages of the
// wanted type carrying the tunnel's identifier. wantType is the echo type this
// side expects from its peer (request or, with the reply split, reply); it is
// ignored for tcp. udp never uses this path (it receives on an ordinary socket).
func spoofBPFProgram(profile SpoofProfile, port uint16, wantType byte) ([]bpf.RawInstruction, error) {
	var prog []bpf.Instruction
	switch {
	case profile == SpoofProfileTCP:
		prog = []bpf.Instruction{
			bpf.LoadMemShift{Off: 0},          // X = IPv4 header length
			bpf.LoadIndirect{Off: 2, Size: 2}, // A = TCP destination port
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(port), SkipTrue: 0, SkipFalse: 1},
			bpf.RetConstant{Val: bpfAccept},
			bpf.RetConstant{Val: 0},
		}
	case profile.isICMPFamily():
		// Keep only echoes of the wanted type whose identifier is this tunnel's.
		// The echo header sits at the same offsets whether the payload is ICMP
		// (proto 1) or ICMPv6-in-IPv4 (proto 58), so one program serves both;
		// only the type number differs. When both ends send requests, wantType is
		// the request type, so the kernel's automatic reply is dropped here in the
		// kernel rather than in the read loop.
		prog = []bpf.Instruction{
			bpf.LoadMemShift{Off: 0},                                               // X = IPv4 header length
			bpf.LoadIndirect{Off: 0, Size: 1},                                      // A = echo type
			bpf.JumpIf{Cond: bpf.JumpNotEqual, Val: uint32(wantType), SkipTrue: 3}, // wrong type -> drop
			bpf.LoadIndirect{Off: 4, Size: 2},                                      // A = echo identifier
			bpf.JumpIf{Cond: bpf.JumpNotEqual, Val: uint32(port), SkipTrue: 1},     // wrong id -> drop
			bpf.RetConstant{Val: bpfAccept},
			bpf.RetConstant{Val: 0},
		}
	default:
		return nil, fmt.Errorf("spoof: no BPF program for profile %q", profile)
	}
	return bpf.Assemble(prog)
}
