package l3

import (
	"bytes"
	"testing"
)

// A minimal but well-formed IPv4 header, enough that the version nibble and
// length are real.
func ipv4Packet(payload ...byte) []byte {
	pkt := []byte{
		0x45, 0x00, 0x00, 0x1c,
		0x00, 0x00, 0x40, 0x00,
		0x40, 0x11, 0x00, 0x00,
		10, 10, 0, 1,
		10, 10, 0, 2,
	}
	return append(pkt, payload...)
}

func ipv6Packet(payload ...byte) []byte {
	pkt := make([]byte, 40)
	pkt[0] = 0x60
	return append(pkt, payload...)
}

func TestEncapRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		encap    string
		greKey   uint32
		overhead int
	}{
		// The two names a config can still carry, both of which are GRE now.
		{"a config that says ipip", "ipip", 0, 4},
		{"a config that says nothing", "", 0, 4},
		{"gre unkeyed", "gre", 0, 4},
		{"gre keyed", "gre", 0xDEADBEEF, 8},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc, err := NewEncap(tc.encap, tc.greKey)
			if err != nil {
				t.Fatalf("NewEncap: %v", err)
			}
			if enc.Overhead() != tc.overhead {
				t.Fatalf("Overhead = %d, want %d", enc.Overhead(), tc.overhead)
			}

			for _, pkt := range [][]byte{ipv4Packet(1, 2, 3), ipv6Packet(9, 9)} {
				frame, err := enc.Wrap(nil, pkt)
				if err != nil {
					t.Fatalf("Wrap: %v", err)
				}
				if len(frame) != len(pkt)+tc.overhead {
					t.Fatalf("wrapped length %d, want %d", len(frame), len(pkt)+tc.overhead)
				}
				back, err := enc.Unwrap(frame)
				if err != nil {
					t.Fatalf("Unwrap: %v", err)
				}
				if !bytes.Equal(back, pkt) {
					t.Fatalf("round trip changed the packet")
				}
			}
		})
	}
}

// Wrap must append rather than overwrite, because the send path hands it a
// buffer that already holds nothing but is reused across packets.
func TestEncapWrapAppends(t *testing.T) {
	enc, err := NewEncap("gre", 0)
	if err != nil {
		t.Fatalf("NewEncap: %v", err)
	}
	prefix := []byte("keep me")
	out, err := enc.Wrap(prefix, ipv4Packet())
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if !bytes.HasPrefix(out, prefix) {
		t.Fatal("Wrap discarded what was already in the buffer")
	}
}

func TestEncapRejectsNonIP(t *testing.T) {
	for _, name := range []string{"ipip", "gre"} {
		enc, err := NewEncap(name, 0)
		if err != nil {
			t.Fatalf("NewEncap: %v", err)
		}
		if _, err := enc.Wrap(nil, []byte{0x00, 0x01, 0x02}); err == nil {
			t.Fatalf("%s wrapped a packet whose version nibble is neither 4 nor 6", name)
		}
		if _, err := enc.Wrap(nil, nil); err == nil {
			t.Fatalf("%s wrapped an empty packet", name)
		}
	}
}

func TestGREKeyMismatchIsRejected(t *testing.T) {
	keyed, err := NewEncap("gre", 1234)
	if err != nil {
		t.Fatalf("NewEncap: %v", err)
	}
	other, err := NewEncap("gre", 5678)
	if err != nil {
		t.Fatalf("NewEncap: %v", err)
	}
	unkeyed, err := NewEncap("gre", 0)
	if err != nil {
		t.Fatalf("NewEncap: %v", err)
	}

	frame, err := keyed.Wrap(nil, ipv4Packet())
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if _, err := other.Unwrap(frame); err == nil {
		t.Fatal("a frame keyed 1234 was accepted by a tunnel keyed 5678")
	}
	if _, err := unkeyed.Unwrap(frame); err == nil {
		t.Fatal("a keyed frame was accepted by an unkeyed tunnel")
	}

	plain, err := unkeyed.Wrap(nil, ipv4Packet())
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if _, err := keyed.Unwrap(plain); err == nil {
		t.Fatal("an unkeyed frame was accepted by a keyed tunnel")
	}
}

func TestGRERejectsMalformedFrames(t *testing.T) {
	enc, err := NewEncap("gre", 0)
	if err != nil {
		t.Fatalf("NewEncap: %v", err)
	}
	cases := map[string][]byte{
		"truncated":         {0x00, 0x00},
		"unsupported flags": append([]byte{0x80, 0x00, 0x08, 0x00}, ipv4Packet()...),
		"unknown protocol":  append([]byte{0x00, 0x00, 0x12, 0x34}, ipv4Packet()...),
		"protocol disagrees with payload": append(
			[]byte{0x00, 0x00, 0x86, 0xDD}, ipv4Packet()...),
		"key bit set but truncated": {0x20, 0x00, 0x08, 0x00, 0x01},
		"empty payload":             {0x00, 0x00, 0x08, 0x00},
	}
	for name, frame := range cases {
		if _, err := enc.Unwrap(frame); err == nil {
			t.Fatalf("Unwrap accepted a %s frame", name)
		}
	}
}

func TestNewEncapRejectsUnknownName(t *testing.T) {
	if _, err := NewEncap("vxlan", 0); err == nil {
		t.Fatal("NewEncap accepted an encapsulation it does not implement")
	}
}

// One encapsulation, and nothing that can put a second one back.
//
// Two was a way for the ends to disagree, and the disagreement was expensive
// out of all proportion to what the choice bought: four bytes, against a tunnel
// that came up, reported a peer, logged nothing and carried nothing. Every
// name a config can hold now produces the same wrapping and announces the same
// identity, so there is nothing left to mismatch.
func TestThereIsOnlyOneEncapsulation(t *testing.T) {
	var ids []string
	for _, name := range []string{"", "ipip", "IPIP", " gre ", "gre"} {
		e, err := NewEncap(name, 0)
		if err != nil {
			t.Fatalf("NewEncap(%q): %v", name, err)
		}
		if e.Name() != "gre" {
			t.Errorf("NewEncap(%q) built %q, not gre", name, e.Name())
		}
		ids = append(ids, encapID(e))
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("the names announce different identities: %v", ids)
		}
	}
	// And an unknown one is still refused rather than quietly becoming gre.
	if _, err := NewEncap("vxlan", 0); err == nil {
		t.Error("an encapsulation nothing implements was accepted")
	}
}
