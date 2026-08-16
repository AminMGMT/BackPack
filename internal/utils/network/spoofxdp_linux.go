//go:build linux

package network

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
)

// The XDP receive fast path.
//
// An eBPF program attached at the NIC's XDP hook matches this tunnel's forged
// packets in the kernel — by IP protocol, the L4 demux port (or ICMP echo id),
// and optionally the peer's forged source — copies each match's L4 segment into
// a BPF ring buffer, and XDP_DROPs it so the ordinary network stack never
// allocates an sk_buff for it. Everything else is XDP_PASSed untouched. Userland
// reads the segments out of the ring, so the receive path never touches the
// socket layer.
//
// It is built with cilium/ebpf — pure Go, no clang, no libbpf, no CGo — and the
// program below is assembled instruction by instruction with cilium/ebpf/asm.
//
// It is best-effort and opt-in. Any failure (kernel too old for the ring buffer
// or bpf_xdp_load_bytes helpers, no CAP_BPF, the verifier rejecting the program,
// a driver without XDP) returns an error, and the carrier falls back to its
// ordinary raw/UDP receive — so turning it on can never cost a working tunnel.
//
// One caveat it shares with every XDP capture: XDP runs before IP reassembly, so
// it sees individual fragments, not the reassembled datagram the raw-socket path
// gets. The carrier sizes its datagrams under the tunnel MTU, so they are not
// fragmented in normal use; a rare oversize packet is not reassembled here and
// is dropped, which the reliable layer above (KCP) or the inner transport
// (WireGuard, in relay mode) recovers.

// BPF helper numbers used by the program, in case the installed cilium/ebpf
// predates the named builtins.
const (
	fnRingbufReserve = asm.BuiltinFunc(131)
	fnRingbufSubmit  = asm.BuiltinFunc(132)
	fnRingbufDiscard = asm.BuiltinFunc(133)
	fnXDPLoadBytes   = asm.BuiltinFunc(189)
)

// XDP return codes.
const (
	xdpDrop = 1
	xdpPass = 2
)

// The ring-buffer record layout the program writes and read() parses:
//
//	[0:4]  forged source IPv4, in wire order
//	[4:8]  L4 segment length (native-endian u32; only the low 16 bits are used)
//	[8:8+len] the L4 segment (udp/tcp/icmp header + payload, or the bare IP body
//	          for ipip/gre)
const (
	xdpRecHdrLen  = 8
	xdpRecSize    = xdpRecHdrLen + maxXDPPayload
	ethHdrLen     = 14
	ethTypeOffLo  = 12 // ethertype high byte within the frame
	ipVerIHLOff   = ethHdrLen + 0
	ipProtoOff    = ethHdrLen + 9
	ipSrcOff      = ethHdrLen + 12
	headerLoadLen = ethHdrLen + 20 // eth + a 20-byte (minimum) IPv4 header
)

type spoofXDPReceiver struct {
	prog *ebpf.Program
	ring *ebpf.Map
	lnk  link.Link
	rd   *ringbuf.Reader
	rec  ringbuf.Record
}

// newSpoofXDPReceiver assembles, loads and attaches the XDP program for one
// tunnel, and opens a ring-buffer reader over its output.
func newSpoofXDPReceiver(c spoofXDPConfig) (*spoofXDPReceiver, error) {
	iface, err := net.InterfaceByName(c.iface)
	if err != nil {
		return nil, fmt.Errorf("spoof xdp: interface %q: %w", c.iface, err)
	}
	// ipip/gre carry no port to demux on, so without a pinned forged source the
	// program could not tell the tunnel's packets from any other of that protocol
	// and would wrongly consume them. Refuse rather than eat unrelated traffic;
	// the caller falls back to the raw path, which has the same requirement.
	if c.portOff < 0 && c.expectSrc == nil {
		return nil, errors.New("spoof xdp: a portless profile (ipip/gre) needs spoof_peer_src_ip set to use the XDP path")
	}

	ring, err := ebpf.NewMap(&ebpf.MapSpec{
		Type:       ebpf.RingBuf,
		MaxEntries: ringBytes(c.sockBuf),
	})
	if err != nil {
		return nil, fmt.Errorf("spoof xdp: ring buffer: %w", err)
	}

	prog, err := ebpf.NewProgram(&ebpf.ProgramSpec{
		Name:         "backpack_spoof",
		Type:         ebpf.XDP,
		License:      "GPL", // the ringbuf and xdp_load_bytes helpers are GPL-only
		Instructions: buildXDPProgram(c, ring.FD()),
	})
	if err != nil {
		ring.Close()
		// %+v so a *ebpf.VerifierError prints its full log rather than the
		// one-line summary — that log is what pinpoints a rejected instruction
		// when tuning the program on a real kernel.
		return nil, fmt.Errorf("spoof xdp: load program (kernel too old, no CAP_BPF, or verifier rejection): %+v", err)
	}

	lnk, err := link.AttachXDP(link.XDPOptions{Program: prog, Interface: iface.Index})
	if err != nil {
		prog.Close()
		ring.Close()
		return nil, fmt.Errorf("spoof xdp: attach to %q (driver without XDP?): %w", c.iface, err)
	}

	rd, err := ringbuf.NewReader(ring)
	if err != nil {
		lnk.Close()
		prog.Close()
		ring.Close()
		return nil, fmt.Errorf("spoof xdp: ring reader: %w", err)
	}

	return &spoofXDPReceiver{prog: prog, ring: ring, lnk: lnk, rd: rd}, nil
}

// read returns the next captured L4 segment, copied into dst, along with the
// forged source it arrived from. It blocks until a packet lands or the reader is
// closed (which surfaces as an error, unblocking the carrier's ReadFrom).
func (r *spoofXDPReceiver) read(dst []byte) (net.IP, int, error) {
	for {
		if err := r.rd.ReadInto(&r.rec); err != nil {
			return nil, 0, err
		}
		s := r.rec.RawSample
		if len(s) < xdpRecHdrLen {
			continue
		}
		n := int(binary.NativeEndian.Uint32(s[4:8]))
		if n <= 0 || xdpRecHdrLen+n > len(s) {
			continue
		}
		src := net.IPv4(s[0], s[1], s[2], s[3]).To4()
		return src, copy(dst, s[xdpRecHdrLen:xdpRecHdrLen+n]), nil
	}
}

// setReadDeadline pushes KCP's read deadline down to the ring reader, so a
// blocked read wakes on time exactly as it would on a socket. A passed deadline
// surfaces from ReadInto as os.ErrDeadlineExceeded, which is a timeout net.Error,
// so KCP treats it the same as a socket read timeout.
func (r *spoofXDPReceiver) setReadDeadline(t time.Time) { r.rd.SetDeadline(t) }

func (r *spoofXDPReceiver) Close() error {
	if r.rd != nil {
		r.rd.Close()
	}
	if r.lnk != nil {
		r.lnk.Close()
	}
	if r.prog != nil {
		r.prog.Close()
	}
	if r.ring != nil {
		r.ring.Close()
	}
	return nil
}

// ringBytes rounds a socket-buffer hint up to a power of two suitable for a BPF
// ring buffer (which the kernel requires be a power-of-two multiple of the page
// size), clamped to a sane range.
func ringBytes(hint int) uint32 {
	const min, max = 1 << 20, 16 << 20
	if hint < min {
		hint = min
	}
	if hint > max {
		hint = max
	}
	n := uint32(min)
	for n < uint32(hint) {
		n <<= 1
	}
	return n
}

// buildXDPProgram assembles the XDP filter for one tunnel. mapFD is the ring
// buffer's file descriptor, embedded into the map-load instruction.
//
// Registers: R6 holds the ctx for the whole program; R7 the reserved ring-buffer
// record once taken; R8 the computed L4 offset; R9 the payload length. Scratch
// lives on the stack at R10-40 (a 34-byte header snapshot) and R10-44 (2 bytes
// for the port field).
func buildXDPProgram(c spoofXDPConfig, mapFD int) asm.Instructions {
	const (
		hdrBuf  = -40 // 34-byte header snapshot base
		portBuf = -44 // 2-byte L4 match field
	)
	insns := asm.Instructions{
		asm.Mov.Reg(asm.R6, asm.R1), // save ctx

		// bpf_xdp_load_bytes(ctx, 0, &hdr, 34) — snapshot eth + IPv4 header.
		asm.Mov.Reg(asm.R1, asm.R6),
		asm.Mov.Imm(asm.R2, 0),
		asm.Mov.Reg(asm.R3, asm.R10),
		asm.Add.Imm(asm.R3, hdrBuf),
		asm.Mov.Imm(asm.R4, headerLoadLen),
		fnXDPLoadBytes.Call(),
		asm.JNE.Imm(asm.R0, 0, "pass"), // short/error → pass it on

		// ethertype == IPv4 (0x0800)? two byte compares, endianness-free.
		asm.LoadMem(asm.R1, asm.R10, hdrBuf+ethTypeOffLo, asm.Byte),
		asm.JNE.Imm(asm.R1, 0x08, "pass"),
		asm.LoadMem(asm.R1, asm.R10, hdrBuf+ethTypeOffLo+1, asm.Byte),
		asm.JNE.Imm(asm.R1, 0x00, "pass"),

		// IP protocol == the profile's?
		asm.LoadMem(asm.R1, asm.R10, hdrBuf+ipProtoOff, asm.Byte),
		asm.JNE.Imm(asm.R1, int32(c.proto), "pass"),
	}

	// Optional forged-source pin. The 4 source bytes are compared as one
	// native-endian word against the same interpretation of expectSrc.
	if src4 := c.expectSrc.To4(); src4 != nil {
		want := int32(binary.NativeEndian.Uint32(src4))
		insns = append(insns,
			asm.LoadMem(asm.R1, asm.R10, hdrBuf+ipSrcOff, asm.Word),
			asm.JNE.Imm(asm.R1, want, "pass"),
		)
	}

	// L4 offset = 14 + (IHL & 0x0f) * 4.
	insns = append(insns,
		asm.LoadMem(asm.R8, asm.R10, hdrBuf+ipVerIHLOff, asm.Byte),
		asm.And.Imm(asm.R8, 0x0f),
		asm.LSh.Imm(asm.R8, 2),
		asm.Add.Imm(asm.R8, ethHdrLen),
	)

	// Optional port / echo-id match, at l4off + portOff.
	if c.portOff >= 0 {
		want := int32(uint16(c.port>>8) | uint16(c.port&0xff)<<8) // wire order as a native u16
		insns = append(insns,
			asm.Mov.Reg(asm.R1, asm.R6),
			asm.Mov.Reg(asm.R2, asm.R8),
			asm.Add.Imm(asm.R2, int32(c.portOff)),
			asm.Mov.Reg(asm.R3, asm.R10),
			asm.Add.Imm(asm.R3, portBuf),
			asm.Mov.Imm(asm.R4, 2),
			fnXDPLoadBytes.Call(),
			asm.JNE.Imm(asm.R0, 0, "pass"),
			asm.LoadMem(asm.R1, asm.R10, portBuf, asm.Half),
			asm.JNE.Imm(asm.R1, want, "pass"),
		)
	}

	// payload length = (data_end - data) - l4off, clamped to (0, maxXDPPayload].
	insns = append(insns,
		asm.LoadMem(asm.R1, asm.R6, 0, asm.Word), // data
		asm.LoadMem(asm.R2, asm.R6, 4, asm.Word), // data_end
		asm.Sub.Reg(asm.R2, asm.R1),              // total length
		asm.Mov.Reg(asm.R9, asm.R2),
		asm.Sub.Reg(asm.R9, asm.R8), // minus l4off
		asm.JSLE.Imm(asm.R9, 0, "pass"),
		asm.JGT.Imm(asm.R9, maxXDPPayload, "pass"), // oversize/fragment → leave it

		// rec = bpf_ringbuf_reserve(map, xdpRecSize, 0).
		asm.LoadMapPtr(asm.R1, mapFD),
		asm.Mov.Imm(asm.R2, xdpRecSize),
		asm.Mov.Imm(asm.R3, 0),
		fnRingbufReserve.Call(),
		asm.JEq.Imm(asm.R0, 0, "pass"), // ring full → don't drop, let it pass
		asm.Mov.Reg(asm.R7, asm.R0),

		// rec[0:4] = src IPv4, rec[4:8] = length.
		asm.LoadMem(asm.R1, asm.R10, hdrBuf+ipSrcOff, asm.Word),
		asm.StoreMem(asm.R7, 0, asm.R1, asm.Word),
		asm.StoreMem(asm.R7, 4, asm.R9, asm.Word),

		// bpf_xdp_load_bytes(ctx, l4off, rec+8, len).
		asm.Mov.Reg(asm.R1, asm.R6),
		asm.Mov.Reg(asm.R2, asm.R8),
		asm.Mov.Reg(asm.R3, asm.R7),
		asm.Add.Imm(asm.R3, xdpRecHdrLen),
		asm.Mov.Reg(asm.R4, asm.R9),
		fnXDPLoadBytes.Call(),
		asm.JNE.Imm(asm.R0, 0, "discard"),

		// bpf_ringbuf_submit(rec, 0); return XDP_DROP.
		asm.Mov.Reg(asm.R1, asm.R7),
		asm.Mov.Imm(asm.R2, 0),
		fnRingbufSubmit.Call(),
		asm.Mov.Imm(asm.R0, xdpDrop),
		asm.Return(),

		// discard: bpf_ringbuf_discard(rec, 0); fall through to pass.
		asm.Mov.Reg(asm.R1, asm.R7).WithSymbol("discard"),
		asm.Mov.Imm(asm.R2, 0),
		fnRingbufDiscard.Call(),

		// pass: return XDP_PASS.
		asm.Mov.Imm(asm.R0, xdpPass).WithSymbol("pass"),
		asm.Return(),
	)
	return insns
}
