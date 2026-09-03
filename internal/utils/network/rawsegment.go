package network

import "net"

// Building a TCP segment by hand, for something other than the pck carrier.
//
// The framing in pckframe.go is careful in ways that took work to get right:
// timestamps on every segment, a window that scales, an acknowledgement that
// tracks what has actually arrived, a Don't Fragment bit and an EF marking —
// the details that separate a segment a stack produced from one a program
// assembled. Anything else that has to put a believable TCP segment on the wire
// wants exactly that, and writing a second version of it would mean a second
// version to get wrong.
//
// So this file exports it, and adds nothing. It is a separate file on purpose:
// the pck carrier's own sources are not edited to make room for another user,
// so whatever else comes to depend on this, pck's behaviour cannot change
// because of it.

// RawSegmentOverhead is what the IP and TCP headers cost, exactly as the pck
// carrier counts them.
func RawSegmentOverhead() int { return pckOverhead }

// RawSegment is one parsed TCP segment: who sent it, where it was going, where
// it sits in the stream, and what it carried.
type RawSegment struct {
	SrcIP   net.IP
	SrcPort uint16
	DstPort uint16
	Seq     uint32
	TSVal   uint32
	Payload []byte
}

// BuildRawTCP assembles a TCP segment with its options and checksum.
func BuildRawTCP(srcPort, dstPort uint16, seq, ack uint32, flags TCPFlags,
	tsVal, tsEcr uint32, src, dst net.IP, payload []byte) []byte {
	return buildPckTCP(srcPort, dstPort, seq, ack, flags, tsVal, tsEcr, src, dst, payload)
}

// BuildRawIPv4 wraps an L4 payload in an IPv4 header.
func BuildRawIPv4(id uint16, src, dst net.IP, l4 []byte) []byte {
	return buildPckIPv4(id, src, dst, l4)
}

// BuildRawEthernet wraps an IP packet in an Ethernet header.
func BuildRawEthernet(dstMAC, srcMAC net.HardwareAddr, ip []byte) []byte {
	return buildPckEthernet(dstMAC, srcMAC, ip)
}

// ParseRawSegment pulls a TCP segment addressed to wantPort out of a captured
// frame. linkLen is 14 for an Ethernet capture and 0 for a cooked one.
func ParseRawSegment(frame []byte, linkLen int, wantPort uint16) (RawSegment, bool) {
	seg, ok := parsePckFrame(frame, linkLen, wantPort)
	if !ok {
		return RawSegment{}, false
	}
	return RawSegment{
		SrcIP:   seg.SrcIP,
		SrcPort: seg.SrcPort,
		DstPort: seg.DstPort,
		Seq:     seg.Seq,
		TSVal:   seg.TSVal,
		Payload: seg.Payload,
	}, true
}
