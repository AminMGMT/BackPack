package l3

import "testing"

// The l3 header parser, fuzzed.
//
// This is the first thing every datagram the tunnel receives goes through, and
// the datagrams are whatever arrives on an open UDP port: a scanner, a stale
// peer, a censor's probe, or someone who has worked out which port to poke. It
// is bounds-checked by hand, and hand-checked bounds are exactly what a fuzzer
// is for.
//
// The contract is narrow and absolute: for any bytes at all, parseHeader either
// returns an error or returns a header and a body that is a slice of the input.
// It must never panic, and it must never hand back more bytes than it was
// given — a body longer than the input would send the crypto layer reading off
// the end of the buffer.
func FuzzParseHeader(f *testing.F) {
	// Seeds: one of each kind, a truncated one, an unknown kind, and empty.
	f.Add([]byte{})
	f.Add([]byte{typeData})
	f.Add(append([]byte{typeInit}, make([]byte, headerLen)...))
	f.Add(append([]byte{typeResp}, make([]byte, headerLen+64)...))
	f.Add(append([]byte{typeData}, make([]byte, headerLen+1500)...))
	f.Add(append([]byte{typeProbe}, make([]byte, headerLen)...))
	f.Add(append([]byte{typeProbeAck}, make([]byte, headerLen)...))
	f.Add(append([]byte{0xFF}, make([]byte, headerLen)...)) // unknown kind
	f.Add(make([]byte, headerLen-1))                        // one byte short

	f.Fuzz(func(t *testing.T, data []byte) {
		h, body, err := parseHeader(data)
		if err != nil {
			// A rejected datagram must hand back nothing to act on.
			if body != nil {
				t.Fatalf("parseHeader rejected %d bytes and still returned a %d-byte body",
					len(data), len(body))
			}
			return
		}

		// Accepting implies there was a header there.
		if len(data) < headerLen {
			t.Fatalf("accepted %d bytes, which is shorter than a header", len(data))
		}
		// The body is the remainder and nothing more. A body longer than the
		// input is the bug that would send the crypto layer off the end of the
		// buffer.
		if len(body) != len(data)-headerLen {
			t.Fatalf("body is %d bytes from a %d-byte datagram; expected %d",
				len(body), len(data), len(data)-headerLen)
		}
		// Only the five kinds the protocol has may be accepted; anything else
		// reaching the switch in the receive pump is a datagram nothing owns.
		switch h.kind {
		case typeInit, typeResp, typeData, typeProbe, typeProbeAck:
		default:
			t.Fatalf("accepted an unknown kind %#x", h.kind)
		}
	})
}

// The reply payload the responder sends back, fuzzed.
//
// It carries the negotiated protocol version and the encapsulation id, and it
// is read from a peer that has proved it holds the token — but "holds the
// token" is not "is well behaved", and a compromised or simply older peer can
// send anything here.
func FuzzParseReplyPayload(f *testing.F) {
	f.Add("")
	f.Add("udp")
	f.Add("udp\x00v1,1")
	f.Add("\x00")
	f.Add("udp\x00v999999999999999999999999,1")
	f.Add("udp\x00v1,1\x00v1,1")
	f.Add("udp\x00v")
	f.Add("udp\x00vx,y")
	f.Add("udp\x00v-1,-1")

	f.Fuzz(func(t *testing.T, payload string) {
		encap, theirs, sawMine, err := parseReplyPayload(payload)
		if err != nil {
			// A rejected reply must not also hand back something to act on:
			// an encapsulation id read out of a malformed block would be
			// compared against ours and reported as a misconfiguration.
			if encap != "" || theirs != 0 || sawMine != 0 {
				t.Fatalf("rejected %q and still returned (%q, %d, %d)",
					payload, encap, theirs, sawMine)
			}
			return
		}
		// A version this build cannot reason about must not come back as
		// accepted. agreedVersion takes the lower of the two, so a negative
		// one would agree on a version that does not exist.
		if theirs < 0 || sawMine < 0 {
			t.Fatalf("accepted %q with a negative version (%d, %d)", payload, theirs, sawMine)
		}
		if got := agreedVersion(versionCurrent, theirs); got < 0 {
			t.Fatalf("agreedVersion(%d, %d) = %d", versionCurrent, theirs, got)
		}
	})
}
