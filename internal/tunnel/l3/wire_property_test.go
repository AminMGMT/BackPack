package l3

import (
	"bytes"
	"testing"
)

// Round-trip properties of the wire header.
//
// The fuzz targets next door ask whether the parser survives arbitrary bytes.
// These ask the other half, which is the half a fuzzer cannot: that a header
// this build *writes* is a header this build reads back unchanged.
//
// It matters because the two halves are read by different machines. A change
// that shifts a field by a byte keeps both sides self-consistent and breaks
// every existing pair — and the encoder and the decoder both being wrong in the
// same way is exactly what a test of one of them alone will not notice.
//
// These are properties rather than examples: the assertion holds for every
// input in the space, not for the three somebody happened to pick.

// Every kind the protocol has, so a new one added without a round trip fails
// here rather than on a tunnel.
var allKinds = []byte{typeInit, typeResp, typeData, typeProbe, typeProbeAck}

func TestAHeaderSurvivesTheRoundTripForEveryKind(t *testing.T) {
	// Values chosen to catch a field written at the wrong width or offset: the
	// extremes, and patterns that differ in every byte.
	sessions := []uint32{0, 1, 0x7FFFFFFF, 0x80000000, 0xFFFFFFFF, 0xDEADBEEF, 0x01020304}
	counters := []uint64{
		0, 1, 0xFF, 0x100, 0x7FFFFFFFFFFFFFFF, 0x8000000000000000,
		0xFFFFFFFFFFFFFFFF, 0x0102030405060708,
	}

	for _, kind := range allKinds {
		for _, session := range sessions {
			for _, counter := range counters {
				in := header{kind: kind, session: session, counter: counter}

				buf := make([]byte, headerLen+8)
				in.put(buf)

				out, body, err := parseHeader(buf)
				if err != nil {
					t.Fatalf("a header this build wrote was rejected: %v (%+v)", err, in)
				}
				if out != in {
					t.Fatalf("round trip changed the header:\n in: %+v\nout: %+v", in, out)
				}
				if len(body) != 8 {
					t.Fatalf("body is %d bytes, want the 8 that followed the header", len(body))
				}
			}
		}
	}
}

// bytes() and put() must agree. They are two ways of writing the same header —
// one for the AEAD's additional data, one into the send buffer — and the AEAD
// binds what it was given. If they ever disagree, every packet authenticates
// against a header different from the one on the wire, and the far end rejects
// all of them with nothing to say about why.
func TestTheTwoWaysOfWritingAHeaderAgree(t *testing.T) {
	for _, kind := range allKinds {
		in := header{kind: kind, session: 0xA1B2C3D4, counter: 0x0102030405060708}

		viaPut := make([]byte, headerLen)
		in.put(viaPut)
		viaBytes := in.bytes()

		if !bytes.Equal(viaPut, viaBytes[:]) {
			t.Fatalf("put and bytes disagree for kind %#x:\n put: %x\nbytes: %x",
				kind, viaPut, viaBytes[:])
		}
	}
}

// The body must alias the buffer rather than copy it — the receive path relies
// on that, and a change to a copy would be a silent per-packet allocation on
// the hottest path in the engine.
func TestTheBodyAliasesTheBufferItCameFrom(t *testing.T) {
	buf := make([]byte, headerLen+4)
	header{kind: typeData}.put(buf)
	copy(buf[headerLen:], []byte{1, 2, 3, 4})

	_, body, err := parseHeader(buf)
	if err != nil {
		t.Fatalf("parseHeader: %v", err)
	}
	buf[headerLen] = 9
	if body[0] != 9 {
		t.Fatal("the body is a copy of the buffer rather than a window onto it; " +
			"that is an allocation per packet on the receive path")
	}
}

// A datagram exactly one byte short of a header must be refused, and one
// exactly a header long must be accepted with an empty body. Off-by-one at the
// boundary is the classic failure in a hand-checked parser.
func TestTheHeaderLengthBoundaryIsExact(t *testing.T) {
	full := make([]byte, headerLen)
	header{kind: typeData}.put(full)

	if _, body, err := parseHeader(full); err != nil {
		t.Fatalf("a datagram exactly one header long was refused: %v", err)
	} else if len(body) != 0 {
		t.Fatalf("body is %d bytes for a header-only datagram", len(body))
	}

	if _, _, err := parseHeader(full[:headerLen-1]); err == nil {
		t.Fatal("a datagram one byte short of a header was accepted")
	}
}

// The reply payload the responder sends is the other codec on this wire, and it
// carries the negotiated version. Same property: what this build writes, this
// build reads.
func TestTheReplyPayloadSurvivesTheRoundTrip(t *testing.T) {
	for _, encap := range []string{"", "udp", "gre:7", "ipip"} {
		for _, mine := range []int{versionLegacy, version1, versionCurrent, 7} {
			// saw == versionLegacy is deliberately absent. A dialler that
			// announced nothing is an old build, and the reply to it is the
			// bare encapsulation with no version block — the shape that build
			// expects. There is nothing to round-trip there, and the legacy
			// case has its own test below.
			for _, saw := range []int{version1, versionCurrent, 7} {
				in := replyPayload(encap, mine, saw)

				gotEncap, theirs, sawMine, err := parseReplyPayload(in)
				if err != nil {
					t.Fatalf("a payload this build wrote was rejected: %v (%q)", err, in)
				}
				if gotEncap != encap {
					t.Fatalf("encapsulation %q became %q", encap, gotEncap)
				}
				if theirs != mine || sawMine != saw {
					t.Fatalf("versions (%d, %d) became (%d, %d)", mine, saw, theirs, sawMine)
				}
			}
		}
	}
}

// A legacy peer sends the encapsulation alone, with no version block at all,
// and that has to keep working — an upgrade that refused it would take down
// every tunnel whose other end had not been updated yet.
func TestALegacyReplyIsStillUnderstood(t *testing.T) {
	for _, encap := range []string{"", "udp", "gre:7"} {
		gotEncap, theirs, sawMine, err := parseReplyPayload(encap)
		if err != nil {
			t.Fatalf("a legacy reply %q was refused: %v", encap, err)
		}
		if gotEncap != encap {
			t.Fatalf("encapsulation %q became %q", encap, gotEncap)
		}
		if theirs != versionLegacy || sawMine != versionLegacy {
			t.Fatalf("a peer that announced nothing was read as versions (%d, %d)",
				theirs, sawMine)
		}
	}
}

// Version agreement takes the lower of the two, always. This is the property
// the whole negotiation rests on: two ends that disagree must both arrive at
// the same number, or they are speaking different protocols while believing
// they agreed.
func TestVersionAgreementIsTheLowerOfTheTwo(t *testing.T) {
	for mine := 0; mine <= 4; mine++ {
		for theirs := 0; theirs <= 4; theirs++ {
			got := agreedVersion(mine, theirs)
			want := mine
			if theirs < mine {
				want = theirs
			}
			if got != want {
				t.Fatalf("agreedVersion(%d, %d) = %d, want %d", mine, theirs, got, want)
			}
			// And it is symmetric: the two ends compute it independently and
			// have to land on the same answer.
			if other := agreedVersion(theirs, mine); other != got {
				t.Fatalf("the two ends disagree: %d vs %d for (%d, %d)",
					got, other, mine, theirs)
			}
		}
	}
}
