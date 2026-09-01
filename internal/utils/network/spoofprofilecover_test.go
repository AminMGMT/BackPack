//go:build linux

package network

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every profile has to be handled in all three of the carrier's switches.
//
// The carrier picks its framing with a switch on the send profile and two more
// on the receive profile — one for the ordinary socket path and one for the XDP
// fast path. A profile missing from any of them falls to the default arm, which
// is "strip an L4 header": on a bare profile that eats the first bytes of the
// payload, so the AEAD above rejects every packet. The tunnel comes up, reports
// itself connected, and carries nothing — with nothing in the log, because
// nothing failed.
//
// Those switches cannot be reached without a raw socket, which needs privilege,
// so this reads the source instead. That is a weaker test than sending a packet
// through, and it is the strongest one available here; what it rules out is the
// specific mistake of adding a profile and wiring it into two places out of
// three, which is exactly how a profile comes to exist and not work.

// bareProfiles carry the payload as the IP body with no L4 header at all. They
// are the ones the default arm would silently corrupt.
var bareProfiles = []SpoofProfile{SpoofProfileIPIP, SpoofProfileProto58}

func TestEveryBareProfileIsHandledInAllThreeSwitches(t *testing.T) {
	src, err := os.ReadFile("spoofconn_linux.go")
	if err != nil {
		t.Fatalf("reading the carrier: %v", err)
	}
	body := string(src)

	sendArms := regexp.MustCompile(`case c\.sendProfile [^\n]*\n`).FindAllString(body, -1)
	recvArms := regexp.MustCompile(`case c\.recvProfile [^\n]*\n`).FindAllString(body, -1)

	if len(sendArms) == 0 || len(recvArms) == 0 {
		t.Fatal("the carrier's profile switches could not be found; this test is reading the wrong shape")
	}
	// One send switch, two receive switches — the ordinary path and the XDP one.
	// If that ever changes, the counts below are wrong and this must be looked at
	// rather than quietly passing.
	const wantRecvSwitches = 2

	for _, p := range bareProfiles {
		name := profileConstName(t, p)

		if !anyContains(sendArms, name) {
			t.Errorf("%s is not handled when sending: its payload would get an L4 header the peer does not expect", p)
		}
		if got := countContaining(recvArms, name); got != wantRecvSwitches {
			t.Errorf("%s is handled in %d of the %d receive switches; the missing one would strip bytes off a payload that has no header",
				p, got, wantRecvSwitches)
		}
	}
}

// And the properties that make a profile bare have to agree with the switches:
// no L4 header to build, and no port to filter on.
func TestBareProfilesAgreeWithTheirHeaderLength(t *testing.T) {
	for _, p := range bareProfiles {
		if n := profileL4Len(p); n != 0 {
			t.Errorf("%s is handled as bare but claims a %d-byte L4 header", p, n)
		}
		if p.hasPortDemux() {
			t.Errorf("%s is handled as bare but claims a port to demultiplex on", p)
		}
	}
	// The converse, so this test cannot pass by calling everything bare.
	for _, p := range []SpoofProfile{SpoofProfileUDP, SpoofProfileTCP, SpoofProfileICMP, SpoofProfileICMPv6} {
		if profileL4Len(p) == 0 {
			t.Errorf("%s lost its L4 header", p)
		}
	}
}

// Every profile the parser accepts must be one the menus offer, and the other
// way round. A profile that parses and is never offered is a setting only a
// hand-edited config can reach; one that is offered and does not parse is a
// menu entry that fails at startup.
func TestEveryParsableProfileIsReachable(t *testing.T) {
	offered := []string{"udp", "icmp", "tcp", "icmpv6", "proto58", "ipip", "gre"}

	for _, name := range offered {
		if _, err := ParseSpoofProfile(name); err != nil {
			t.Errorf("%q is offered in the menus and the carrier refuses it: %v", name, err)
		}
	}
	for _, p := range []SpoofProfile{
		SpoofProfileUDP, SpoofProfileTCP, SpoofProfileICMP,
		SpoofProfileICMPv6, SpoofProfileProto58, SpoofProfileIPIP, SpoofProfileGRE,
	} {
		found := false
		for _, name := range offered {
			if string(p) == name {
				found = true
			}
		}
		if !found {
			t.Errorf("the carrier knows %q and no menu offers it", p)
		}
	}
}

// profileConstName maps a profile to the Go identifier the switches are written
// with, so this test breaks loudly if a constant is renamed rather than
// silently finding nothing.
func profileConstName(t *testing.T, p SpoofProfile) string {
	t.Helper()
	switch p {
	case SpoofProfileIPIP:
		return "SpoofProfileIPIP"
	case SpoofProfileProto58:
		return "SpoofProfileProto58"
	case SpoofProfileGRE:
		return "SpoofProfileGRE"
	}
	t.Fatalf("no constant name recorded for %q", p)
	return ""
}

func anyContains(lines []string, want string) bool {
	return countContaining(lines, want) > 0
}

func countContaining(lines []string, want string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, want) {
			n++
		}
	}
	return n
}
