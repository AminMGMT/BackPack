package network

import (
	"testing"

	"golang.org/x/net/ipv4"
)

// The two frame parsers that read straight off a raw socket.
//
// These are the most exposed parsers in the product. A packet socket receives
// every frame on the interface, so the input is not "a datagram from a peer" —
// it is whatever the network puts in front of the machine, from any host, of
// any shape, including deliberately malformed ones. Both are bounds-checked by
// hand against header fields that the sender controls, and hand-checked bounds
// against attacker-controlled lengths is the textbook shape for an
// out-of-bounds read.
//
// Neither may panic on any input, and neither may hand back a payload that is
// not a slice of what it was given.

// parsePckFrame walks an Ethernet header, an IPv4 header with a variable-length
// options field, and a TCP header with its own variable-length options — three
// attacker-controlled lengths in a row, each used to index further into the
// buffer.
func FuzzParsePckFrame(f *testing.F) {
	// A minimal well-formed-ish frame, plus the shapes that break naive
	// bounds checks: empty, header-length-only, an IHL that points past the
	// end, and a data offset that does.
	f.Add([]byte{}, 14, uint16(443))
	f.Add(make([]byte, 14), 14, uint16(443))
	f.Add(make([]byte, 14+ipv4.HeaderLen), 14, uint16(443))
	f.Add(make([]byte, 14+ipv4.HeaderLen+20), 14, uint16(443))
	f.Add(make([]byte, 64), 0, uint16(443))

	// A frame whose IPv4 header claims the maximum options length.
	long := make([]byte, 14+60+60+16)
	long[12], long[13] = 0x08, 0x00 // EtherType IPv4
	long[14] = 0x4F                 // version 4, IHL 15 (60 bytes)
	long[14+9] = 6                  // protocol TCP
	f.Add(long, 14, uint16(443))

	f.Fuzz(func(t *testing.T, frame []byte, linkLen int, wantPort uint16) {
		// linkLen is ours, not the network's — it is 0 or 14 depending on the
		// device. Anything else is not a case this function has to survive, so
		// it is not a case worth burning fuzzing budget on.
		if linkLen != 0 && linkLen != 14 {
			t.Skip()
		}

		seg, ok := parsePckFrame(frame, linkLen, wantPort)
		if !ok {
			return
		}
		// Accepting means there is a payload, and it must live inside the
		// frame it came from. A payload longer than its frame is the read past
		// the end this is here to catch.
		if len(seg.Payload) > len(frame) {
			t.Fatalf("payload is %d bytes out of a %d-byte frame", len(seg.Payload), len(frame))
		}
		// The port it matched has to be the port that was asked for, or the
		// carrier is picking up another connection's traffic.
		if seg.DstPort != wantPort {
			t.Fatalf("accepted a segment for port %d while looking for %d", seg.DstPort, wantPort)
		}
	})
}

// decodeXdiPayload reads the tunnel's bytes out of an ICMP echo body. Every
// ping on the machine reaches it.
func FuzzDecodeXdiPayload(f *testing.F) {
	f.Add([]byte{}, byte('C'))
	f.Add([]byte{1, 2, 3}, byte('C'))
	f.Add([]byte{1, 2, 3, 4}, byte('C'))
	f.Add([]byte{1, 2, 3, 4, 'C'}, byte('C'))
	f.Add([]byte{1, 2, 3, 4, 'S'}, byte('C'))
	f.Add(append([]byte{1, 2, 3, 4, 'C'}, make([]byte, 1400)...), byte('C'))

	tag := [xdiTagLen]byte{1, 2, 3, 4}

	f.Fuzz(func(t *testing.T, data []byte, wantDir byte) {
		payload, ok := decodeXdiPayload(tag, wantDir, data)
		if !ok {
			if payload != nil {
				t.Fatalf("rejected %d bytes and still returned a payload", len(data))
			}
			return
		}
		if len(data) < xdiHeaderLen {
			t.Fatalf("accepted %d bytes, shorter than the header", len(data))
		}
		if len(payload) != len(data)-xdiHeaderLen {
			t.Fatalf("payload is %d bytes from %d bytes of data; expected %d",
				len(payload), len(data), len(data)-xdiHeaderLen)
		}
		// It must only accept this tunnel's own tag and the direction asked
		// for, or two tunnels on one host read each other's traffic.
		for i := 0; i < xdiTagLen; i++ {
			if data[i] != tag[i] {
				t.Fatalf("accepted a frame carrying another tunnel's tag")
			}
		}
		if data[xdiTagLen] != wantDir {
			t.Fatalf("accepted a frame going the wrong way")
		}
	})
}

// The text parsers an operator's config reaches. They are not attacker input,
// but they are the ones that turn a typo into either a clear refusal or a
// silent default, and a panic here takes the engine down at load.
func FuzzParseTCPFlags(f *testing.F) {
	f.Add("")
	f.Add("SYN")
	f.Add("syn,ack")
	f.Add("SYN|ACK")
	f.Add(",,,")
	f.Add("SYN,NOTAFLAG")

	f.Fuzz(func(t *testing.T, s string) {
		_, _ = ParseTCPFlags(s)
	})
}

func FuzzParseSpoofProfile(f *testing.F) {
	f.Add("")
	f.Add("chrome")
	f.Add("CHROME")
	f.Add("not-a-profile")
	f.Add("\x00\x00")

	f.Fuzz(func(t *testing.T, s string) {
		_, _ = ParseSpoofProfile(s)
	})
}
