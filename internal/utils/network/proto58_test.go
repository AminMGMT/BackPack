package network

import "testing"

// proto58 is the icmpv6 profile with the echo header taken off: the same
// protocol number a filter tends to leave open, carrying the payload bare.
// What is worth pinning is that it is genuinely bare and genuinely on 58 —
// getting either wrong produces a tunnel that talks to nothing and says
// nothing about why.

func TestProto58IsBareOnProtocolFiftyEight(t *testing.T) {
	if got := SpoofProfileProto58.ipProtocol(); got != 58 {
		t.Errorf("proto58 rides on protocol %d, want 58", got)
	}
	if got := profileL4Len(SpoofProfileProto58); got != 0 {
		t.Errorf("proto58 has a %d-byte L4 header; it is meant to have none", got)
	}
	// icmpv6 shares the protocol number and does carry a header — that is the
	// whole difference between the two, and it must stay.
	if SpoofProfileICMPv6.ipProtocol() != SpoofProfileProto58.ipProtocol() {
		t.Error("icmpv6 and proto58 no longer share a protocol number")
	}
	if profileL4Len(SpoofProfileICMPv6) == 0 {
		t.Error("icmpv6 lost its echo header, which is the only thing that distinguishes it")
	}
}

// With no L4 header there is no port to filter on, so the receiver must know to
// lean on the source pin instead of building a port filter that matches nothing.
func TestProto58HasNoPortToDemultiplexOn(t *testing.T) {
	if SpoofProfileProto58.hasPortDemux() {
		t.Error("proto58 was treated as carrying a port; the receiver would filter on one that is not there")
	}
	for _, p := range []SpoofProfile{SpoofProfileUDP, SpoofProfileTCP, SpoofProfileICMP, SpoofProfileICMPv6} {
		if !p.hasPortDemux() {
			t.Errorf("%s lost its port demultiplexer", p)
		}
	}
}

// And it has to be spellable in a config, or the profile exists only in Go.
func TestProto58Parses(t *testing.T) {
	got, err := ParseSpoofProfile("proto58")
	if err != nil {
		t.Fatalf("proto58 was refused: %v", err)
	}
	if got != SpoofProfileProto58 {
		t.Errorf("parsed to %q, want proto58", got)
	}
	if _, err := ParseSpoofProfile("proto59"); err == nil {
		t.Error("a profile that does not exist was accepted")
	}
}

// The overhead figure is what the MTU is worked out from, so a bare profile
// must cost exactly the IP header and nothing else.
func TestProto58CostsOnlyTheIPHeader(t *testing.T) {
	bare := spoofOverhead(SpoofProfileProto58)
	ipip := spoofOverhead(SpoofProfileIPIP)
	if bare != ipip {
		t.Errorf("proto58 overhead %d, ipip %d — both are bare and should cost the same", bare, ipip)
	}
	if udp := spoofOverhead(SpoofProfileUDP); udp <= bare {
		t.Errorf("udp overhead %d is not more than a bare profile's %d", udp, bare)
	}
}
