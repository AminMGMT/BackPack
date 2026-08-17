package network

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"

	"golang.org/x/net/bpf"
	"golang.org/x/net/ipv4"
)

// Framing for the packet-level TCP carrier — the "pck" transport.
//
// Every other TCP-family transport hands its bytes to the kernel and lets the
// kernel's TCP stack produce the packets. This one builds the packets itself
// and hands them to the wire, so nothing between the tunnel and the network
// card has an opinion about them. What that buys is spelled out in
// pckconn_linux.go; what it costs is that every field a real stack would fill
// in has to be filled in here, correctly, or the segments are recognisable as
// forgeries at a glance.
//
// That is the whole job of this file, and it is not decoration. A DPI box that
// classifies by the TCP header alone will pick out a segment with no options,
// an acknowledgement number of zero, a source port equal to its destination
// port, or a sequence number that does not advance with the data. Linux sends
// none of those, so neither does this: timestamps on every segment, sequence
// and acknowledgement numbers that track the bytes actually exchanged, a
// window that scales, and flags the operator can vary.
//
// Nothing here is a security boundary — KCP's encryption above it is what makes
// the payload unreadable, and it is unchanged. This layer decides only what the
// flow looks like from outside.
//
// It is deliberately free of syscalls and of build tags so that the packets can
// be assembled, parsed and asserted on in a test on any machine.

const (
	pckEthHeaderLen  = 14
	pckTCPHeaderLen  = 20
	pckTCPOptionsLen = 12 // NOP, NOP, Timestamps — what Linux puts on a data segment

	// pckOverhead is what the framing costs inside the path MTU. The Ethernet
	// header is not counted: it is outside the IP MTU, which is what KCP is
	// sized against.
	pckOverhead = ipv4.HeaderLen + pckTCPHeaderLen + pckTCPOptionsLen

	// pckWindow is the receive window advertised on every segment. 65535 with a
	// window scale of 8 (advertised on the opening segment) is an ordinary
	// figure for a modern host, and a constant window is what a receiver with
	// nothing to report legitimately sends.
	pckWindow = 65535

	etherTypeIPv4 = 0x0800
)

// TCPFlags is one combination of TCP header flags.
type TCPFlags uint8

const (
	FlagFIN TCPFlags = 1 << 0
	FlagSYN TCPFlags = 1 << 1
	FlagRST TCPFlags = 1 << 2
	FlagPSH TCPFlags = 1 << 3
	FlagACK TCPFlags = 1 << 4
	FlagURG TCPFlags = 1 << 5
	FlagECE TCPFlags = 1 << 6
	FlagCWR TCPFlags = 1 << 7
)

// flagLetters maps the single-letter spelling used in configs and in tcpdump
// output to the bit it sets. The order is the one tcpdump prints.
var flagLetters = []struct {
	c rune
	f TCPFlags
}{
	{'F', FlagFIN}, {'S', FlagSYN}, {'R', FlagRST}, {'P', FlagPSH},
	{'A', FlagACK}, {'U', FlagURG}, {'E', FlagECE}, {'C', FlagCWR},
}

// ParseTCPFlags turns a combination like "PA" into its bits. The spelling is
// tcpdump's, so an operator can copy what they see in a capture of real traffic
// straight into the config.
func ParseTCPFlags(s string) (TCPFlags, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("pck: empty TCP flag combination")
	}
	var out TCPFlags
	for _, c := range strings.ToUpper(s) {
		var hit bool
		for _, fl := range flagLetters {
			if fl.c == c {
				out |= fl.f
				hit = true
				break
			}
		}
		if !hit {
			return 0, fmt.Errorf("pck: %q is not a TCP flag — use F, S, R, P, A, U, E or C", string(c))
		}
	}
	return out, nil
}

// String renders the flags back to their letters, for logs and for round-trip
// tests.
func (f TCPFlags) String() string {
	var b strings.Builder
	for _, fl := range flagLetters {
		if f&fl.f != 0 {
			b.WriteRune(fl.c)
		}
	}
	return b.String()
}

// ParseTCPFlagList parses a whole cycle of flag combinations. An empty list
// means the default, which is the one real bulk data carries.
func ParseTCPFlagList(in []string) ([]TCPFlags, error) {
	if len(in) == 0 {
		return []TCPFlags{FlagPSH | FlagACK}, nil
	}
	if len(in) > 64 {
		return nil, fmt.Errorf("pck: at most 64 TCP flag combinations, got %d", len(in))
	}
	out := make([]TCPFlags, 0, len(in))
	for _, s := range in {
		f, err := ParseTCPFlags(s)
		if err != nil {
			return nil, err
		}
		// A segment carrying data with neither ACK nor SYN set is not something
		// a stack emits, and a middlebox that tracks state will notice. RST is
		// refused outright: it means "tear this down", and a flow made of them
		// would be discarded by anything paying attention.
		if f&FlagRST != 0 {
			return nil, fmt.Errorf("pck: RST cannot carry data — a flow of resets is not a flow")
		}
		if f&(FlagACK|FlagSYN) == 0 {
			return nil, fmt.Errorf("pck: %q has neither ACK nor SYN — no stack sends data like that", f)
		}
		out = append(out, f)
	}
	return out, nil
}

// FormatTCPFlagList renders a parsed cycle back to config spelling.
func FormatTCPFlagList(in []TCPFlags) []string {
	out := make([]string, len(in))
	for i, f := range in {
		out[i] = f.String()
	}
	return out
}

// DefaultTCPFlagList is what a tunnel uses when the operator says nothing: the
// flags every segment of an established, data-carrying connection has.
func DefaultTCPFlagList() []string { return []string{"PA"} }

// SuggestedTCPFlagCycles are the combinations worth offering in a menu, with
// what each is for. They are all things an established connection genuinely
// sends, so any of them can carry data without looking wrong on its own; what
// varies is how the flow reads as a whole.
func SuggestedTCPFlagCycles() []struct{ Value, Desc string } {
	return []struct{ Value, Desc string }{
		{"PA", "push + ack — what bulk data carries; the default"},
		{"A", "ack only — reads as a stream being drained rather than pushed"},
		{"PA,A", "alternates the two, as a real transfer does"},
		{"PA,A,PA,PAU", "varied — hardest to match on the flag pattern alone"},
	}
}

// pckSegment is what a received frame turned out to hold.
type pckSegment struct {
	SrcIP   net.IP
	SrcPort uint16
	DstPort uint16
	Seq     uint32
	TSVal   uint32
	Payload []byte
}

// pckFrameBuffers hands out the frame buffers both directions work in: the one
// ReadFrom parses a received frame out of, and the one WriteTo assembles an
// outgoing frame into. It stores *[]byte rather than []byte because putting a
// slice into a sync.Pool converts it to an interface, and that conversion
// allocates — which would put back a good part of what the pool is here to save.
// Sized for the largest frame a packet socket can hand up.
var pckFrameBuffers = sync.Pool{
	New: func() any {
		b := make([]byte, 65536)
		return &b
	},
}

// pckFrameParams is everything that varies between one outgoing frame and the
// next. Grouped into a struct because assemblePckFrame would otherwise take
// eleven positional arguments, half of them uint32, which is a bug waiting to
// be written.
type pckFrameParams struct {
	SrcMAC, DstMAC   net.HardwareAddr
	SrcIP, DstIP     net.IP
	SrcPort, DstPort uint16
	Seq, Ack         uint32
	Flags            TCPFlags
	TSVal, TSEcr     uint32
	ID               uint16
	Payload          []byte
}

// pckFrameLen is the whole frame's size on the wire for a given payload.
func pckFrameLen(payload int) int {
	return pckEthHeaderLen + ipv4.HeaderLen + pckTCPHeaderLen + pckTCPOptionsLen + payload
}

// Offsets of each header within an assembled frame.
const (
	pckIPOffset      = pckEthHeaderLen
	pckTCPOffset     = pckIPOffset + ipv4.HeaderLen
	pckPayloadOffset = pckTCPOffset + pckTCPHeaderLen + pckTCPOptionsLen
)

// assemblePckFrame writes one complete Ethernet + IPv4 + TCP frame into buf and
// returns the slice holding it. buf must be at least pckFrameLen(len(payload)).
//
// It exists because the obvious way to build this — a function per layer, each
// returning a fresh slice — allocated three times and copied the payload three
// times for every packet the tunnel sent. On the pck carrier that cost is paid
// per datagram and cannot be amortised the way it is on UDP: kcp-go only reaches
// for the batching write path (sendmmsg, via x/net) when the socket is a real
// *net.UDPConn, and this carrier is not one, so every packet is its own syscall
// and its own frame. Building in place, into a buffer the caller pools, leaves
// one copy of the payload and no allocation at all.
//
// The layer builders below are kept for the tests, which assert on each header
// independently; a test in this package checks that what this produces is byte
// for byte what they produce.
func assemblePckFrame(buf []byte, p pckFrameParams) []byte {
	total := pckFrameLen(len(p.Payload))
	frame := buf[:total]

	// Ethernet.
	copy(frame[0:6], p.DstMAC)
	copy(frame[6:12], p.SrcMAC)
	binary.BigEndian.PutUint16(frame[12:14], etherTypeIPv4)

	// IPv4. Written before the checksum so the checksum covers it.
	ip := frame[pckIPOffset:pckTCPOffset]
	ip[0] = 4<<4 | 5 // version 4, header length 5 words
	ip[1] = 46 << 2  // DSCP 46 (EF), ECN 0
	binary.BigEndian.PutUint16(ip[2:4], uint16(total-pckEthHeaderLen))
	binary.BigEndian.PutUint16(ip[4:6], p.ID)
	binary.BigEndian.PutUint16(ip[6:8], 0x4000) // Don't Fragment
	ip[8] = 64                                  // TTL
	ip[9] = 6                                   // TCP
	ip[10], ip[11] = 0, 0                       // checksum, zero while computing
	copy(ip[12:16], p.SrcIP.To4())
	copy(ip[16:20], p.DstIP.To4())
	binary.BigEndian.PutUint16(ip[10:12], onesComplement(ip))

	// TCP, then the payload it carries.
	const off = pckTCPHeaderLen + pckTCPOptionsLen
	tcp := frame[pckTCPOffset:]
	binary.BigEndian.PutUint16(tcp[0:2], p.SrcPort)
	binary.BigEndian.PutUint16(tcp[2:4], p.DstPort)
	binary.BigEndian.PutUint32(tcp[4:8], p.Seq)
	binary.BigEndian.PutUint32(tcp[8:12], p.Ack)
	tcp[12] = byte(off/4) << 4 // data offset in 32-bit words
	tcp[13] = byte(p.Flags)
	binary.BigEndian.PutUint16(tcp[14:16], pckWindow)
	tcp[16], tcp[17] = 0, 0 // checksum, zero while computing
	tcp[18], tcp[19] = 0, 0 // urgent pointer
	// NOP, NOP, Timestamps — what Linux puts on a data segment.
	tcp[20], tcp[21] = 1, 1
	tcp[22], tcp[23] = 8, 10
	binary.BigEndian.PutUint32(tcp[24:28], p.TSVal)
	binary.BigEndian.PutUint32(tcp[28:32], p.TSEcr)
	copy(frame[pckPayloadOffset:], p.Payload)
	binary.BigEndian.PutUint16(tcp[16:18], l4Checksum(p.SrcIP, p.DstIP, 6, tcp))

	return frame
}

// buildPckTCP assembles a TCP segment carrying payload, with the options and
// numbering a real stack would produce.
//
// tsVal and tsEcr fill the timestamp option: our clock, and the last one we saw
// from the peer. Echoing the peer's timestamp back is what makes the pair look
// like a conversation rather than two monologues, and it is free — the value is
// already being parsed off every inbound segment.
func buildPckTCP(srcPort, dstPort uint16, seq, ack uint32, flags TCPFlags, tsVal, tsEcr uint32, src, dst net.IP, payload []byte) []byte {
	const off = pckTCPHeaderLen + pckTCPOptionsLen
	b := make([]byte, off+len(payload))

	binary.BigEndian.PutUint16(b[0:2], srcPort)
	binary.BigEndian.PutUint16(b[2:4], dstPort)
	binary.BigEndian.PutUint32(b[4:8], seq)
	binary.BigEndian.PutUint32(b[8:12], ack)
	b[12] = byte(off/4) << 4 // data offset in 32-bit words
	b[13] = byte(flags)
	binary.BigEndian.PutUint16(b[14:16], pckWindow)
	// b[16:18] checksum, left zero for the computation
	// b[18:20] urgent pointer, zero

	// NOP, NOP, Timestamps — byte for byte what Linux puts on a data segment,
	// the two NOPs being the padding that aligns the 10-byte option to a word.
	b[20] = 1 // NOP
	b[21] = 1 // NOP
	b[22] = 8 // Timestamps
	b[23] = 10
	binary.BigEndian.PutUint32(b[24:28], tsVal)
	binary.BigEndian.PutUint32(b[28:32], tsEcr)

	copy(b[off:], payload)
	binary.BigEndian.PutUint16(b[16:18], l4Checksum(src, dst, 6, b))
	return b
}

// buildPckIPv4 wraps an L4 segment in an IPv4 header.
//
// TOS carries DSCP 46 (Expedited Forwarding), the same marking the KCP session
// asks for on an ordinary socket — on this path nothing else would set it,
// because the header is ours to fill. Don't-Fragment is set because that is
// what a modern stack does, and because KCP is already sized to fit.
func buildPckIPv4(id uint16, src, dst net.IP, l4 []byte) []byte {
	b := make([]byte, ipv4.HeaderLen+len(l4))
	b[0] = 4<<4 | 5 // version 4, header length 5 words
	b[1] = 46 << 2  // DSCP 46 (EF), ECN 0
	binary.BigEndian.PutUint16(b[2:4], uint16(len(b)))
	binary.BigEndian.PutUint16(b[4:6], id)
	binary.BigEndian.PutUint16(b[6:8], 0x4000) // Don't Fragment
	b[8] = 64                                  // TTL
	b[9] = 6                                   // TCP
	copy(b[12:16], src.To4())
	copy(b[16:20], dst.To4())
	binary.BigEndian.PutUint16(b[10:12], onesComplement(b[:ipv4.HeaderLen]))
	copy(b[ipv4.HeaderLen:], l4)
	return b
}

// buildPckEthernet prepends the Ethernet header addressing the frame to the
// next hop. The tunnel's packets are routed by the switch and the gateway
// exactly as the kernel's would be; only the decision of what to put in them
// was made here rather than there.
func buildPckEthernet(dstMAC, srcMAC net.HardwareAddr, ip []byte) []byte {
	b := make([]byte, pckEthHeaderLen+len(ip))
	copy(b[0:6], dstMAC)
	copy(b[6:12], srcMAC)
	binary.BigEndian.PutUint16(b[12:14], etherTypeIPv4)
	copy(b[pckEthHeaderLen:], ip)
	return b
}

// parsePckFrame pulls the tunnel's datagram out of a received frame, or reports
// that the frame is not one.
//
// linkLen is the length of the link-layer header the capture includes: 14 on
// Ethernet, 0 on a point-to-point device that has none. Everything is bounds
// checked because this reads straight off the wire, where a frame may be
// anything at all.
func parsePckFrame(frame []byte, linkLen int, wantPort uint16) (pckSegment, bool) {
	var seg pckSegment
	if len(frame) < linkLen+ipv4.HeaderLen {
		return seg, false
	}
	if linkLen == pckEthHeaderLen && binary.BigEndian.Uint16(frame[12:14]) != etherTypeIPv4 {
		return seg, false
	}
	ip := frame[linkLen:]

	if ip[0]>>4 != 4 {
		return seg, false
	}
	ihl := int(ip[0]&0x0f) * 4
	if ihl < ipv4.HeaderLen || len(ip) < ihl {
		return seg, false
	}
	if ip[9] != 6 { // not TCP
		return seg, false
	}
	// A fragment holds only part of the segment, so the header we need may not
	// be in it at all. KCP datagrams are sized not to fragment; anything that
	// did is not ours.
	if binary.BigEndian.Uint16(ip[6:8])&0x1fff != 0 {
		return seg, false
	}
	// The IP header's own length field bounds the packet: a capture may carry
	// padding after it, which is not payload.
	total := int(binary.BigEndian.Uint16(ip[2:4]))
	if total < ihl || total > len(ip) {
		if total > len(ip) {
			return seg, false // truncated capture
		}
		return seg, false
	}
	tcp := ip[ihl:total]
	if len(tcp) < pckTCPHeaderLen {
		return seg, false
	}
	dstPort := binary.BigEndian.Uint16(tcp[2:4])
	if dstPort != wantPort {
		return seg, false
	}
	off := int(tcp[12]>>4) * 4
	if off < pckTCPHeaderLen || off > len(tcp) {
		return seg, false
	}

	seg.SrcIP = net.IP(append([]byte(nil), ip[12:16]...))
	seg.SrcPort = binary.BigEndian.Uint16(tcp[0:2])
	seg.DstPort = dstPort
	seg.Seq = binary.BigEndian.Uint32(tcp[4:8])
	seg.TSVal = tcpTimestamp(tcp[pckTCPHeaderLen:off])
	seg.Payload = tcp[off:]
	return seg, true
}

// tcpTimestamp finds the TSval in a TCP option block, or 0 if there is none.
// The option list is walked properly rather than assumed, because a peer is
// entitled to order its options however it likes.
func tcpTimestamp(opts []byte) uint32 {
	for i := 0; i < len(opts); {
		switch opts[i] {
		case 0: // End of option list
			return 0
		case 1: // NOP
			i++
			continue
		}
		if i+1 >= len(opts) {
			return 0
		}
		l := int(opts[i+1])
		if l < 2 || i+l > len(opts) {
			return 0
		}
		if opts[i] == 8 && l == 10 {
			return binary.BigEndian.Uint32(opts[i+2 : i+6])
		}
		i += l
	}
	return 0
}

// parseMAC accepts a hardware address in any of the spellings a person might
// paste from `ip neigh`, `arp -n` or a cloud console.
func parseMAC(s string) (net.HardwareAddr, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	mac, err := net.ParseMAC(s)
	if err != nil {
		return nil, fmt.Errorf("pck: %q is not a MAC address: %w", s, err)
	}
	if len(mac) != 6 {
		return nil, fmt.Errorf("pck: %q is not a 6-byte Ethernet address", s)
	}
	return mac, nil
}

// pckBPFProgram is the kernel-side filter: IPv4 TCP segments, not fragments,
// addressed to our port. linkLen is 14 on Ethernet and 0 where the capture has
// no link header, which shifts every offset.
func pckBPFProgram(port uint16, linkLen int) ([]bpf.RawInstruction, error) {
	l := uint32(linkLen)

	// Built as a flat list with the leave-on-failure jumps left blank, then
	// patched to point at the reject instruction. Every jump distance changes
	// when a test is added or removed, and hand-counted offsets are how a
	// filter quietly ends up accepting the wrong packet.
	var prog []bpf.Instruction

	if linkLen == pckEthHeaderLen {
		prog = append(prog,
			bpf.LoadAbsolute{Off: 12, Size: 2}, // Ethernet type
			bpf.JumpIf{Cond: bpf.JumpNotEqual, Val: etherTypeIPv4})
	} else {
		// No link header to identify the protocol by, so the IP version nibble
		// is what says this is IPv4 at all.
		prog = append(prog,
			bpf.LoadAbsolute{Off: 0, Size: 1},
			bpf.ALUOpConstant{Op: bpf.ALUOpAnd, Val: 0xf0},
			bpf.JumpIf{Cond: bpf.JumpNotEqual, Val: 0x40})
	}
	prog = append(prog,
		bpf.LoadAbsolute{Off: l + 9, Size: 1}, // IP protocol
		bpf.JumpIf{Cond: bpf.JumpNotEqual, Val: 6},
		bpf.LoadAbsolute{Off: l + 6, Size: 2}, // flags + fragment offset
		bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: 0x1fff},
		bpf.LoadMemShift{Off: l},              // X = IPv4 header length
		bpf.LoadIndirect{Off: l + 2, Size: 2}, // TCP destination port
		bpf.JumpIf{Cond: bpf.JumpNotEqual, Val: uint32(port)},
		bpf.RetConstant{Val: bpfAccept},
		bpf.RetConstant{Val: 0})

	reject := len(prog) - 1
	for i, ins := range prog {
		j, ok := ins.(bpf.JumpIf)
		if !ok {
			continue
		}
		skip := reject - i - 1
		if skip < 0 || skip > 255 {
			return nil, fmt.Errorf("pck: BPF jump out of range")
		}
		j.SkipTrue = uint8(skip)
		prog[i] = j
	}
	return bpf.Assemble(prog)
}
