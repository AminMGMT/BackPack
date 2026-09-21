package l3

import (
	"strings"
	"testing"
)

// Version negotiation, and the compatibility it exists to preserve.
//
// The two ends of a tunnel are two machines and are never updated at the same
// instant. There is always a window where one end is new and the other is not,
// and on these deployments it can be weeks — over a link that may be the only
// way to reach the far side. So every one of these cases is a real deployment,
// not a hypothetical.

// A new dialler and an old listener. The listener ignores the header field the
// dialler announced in, replies in the old shape, and the dialler notices and
// drops to the legacy version rather than failing.
func TestANewDiallerFallsBackForAnOldListener(t *testing.T) {
	const token = "a-version-negotiation-token-0123"

	hs, err := beginHandshake(token, 0, "ipip")
	if err != nil {
		t.Fatalf("beginHandshake: %v", err)
	}
	if hs.announced != versionCurrent {
		t.Errorf("the dialler announced v%d, want v%d", hs.announced, versionCurrent)
	}

	// The datagram carries the version where an old build does not look.
	h, body, err := parseHeader(hs.datagram())
	if err != nil {
		t.Fatalf("parseHeader: %v", err)
	}
	if h.counter != uint64(versionCurrent) {
		t.Errorf("the announcement is not in the header counter: got %d", h.counter)
	}

	// An old listener is respond() with no version: it answers the old shape.
	_, reply, err := respond(token, h.session, body, "ipip")
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	rh, rbody, err := parseHeader(reply)
	if err != nil || rh.kind != typeResp {
		t.Fatalf("reply header = %+v, %v", rh, err)
	}
	if _, err := hs.complete(rbody); err != nil {
		t.Fatalf("a new dialler could not complete against an old listener: %v", err)
	}
	if hs.agreed != versionLegacy {
		t.Errorf("agreed v%d with an old listener, want v%d", hs.agreed, versionLegacy)
	}
}

// An old dialler and a new listener. The dialler announces nothing, so the
// listener must answer in the old shape — a version block would be an
// encapsulation mismatch to a peer that compares the payload whole.
func TestANewListenerAnswersAnOldDiallerInTheOldShape(t *testing.T) {
	reply := replyPayload("ipip", versionLegacy, versionCurrent)
	if reply != "ipip" {
		t.Errorf("a new listener answered an old dialler with %q; anything but the bare "+
			"encapsulation is a mismatch to a peer that compares it whole", reply)
	}
}

// Both new: they agree, and each learns the other's version.
func TestTwoNewEndsAgreeOnTheVersion(t *testing.T) {
	const token = "a-version-negotiation-token-0123"

	hs, err := beginHandshake(token, 0, "gre")
	if err != nil {
		t.Fatalf("beginHandshake: %v", err)
	}
	h, body, err := parseHeader(hs.datagram())
	if err != nil {
		t.Fatalf("parseHeader: %v", err)
	}

	_, reply, err := respondV(token, h.session, int(h.counter), body, "gre")
	if err != nil {
		t.Fatalf("respondV: %v", err)
	}
	_, rbody, err := parseHeader(reply)
	if err != nil {
		t.Fatalf("parseHeader(reply): %v", err)
	}
	if _, err := hs.complete(rbody); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if hs.agreed != versionCurrent {
		t.Errorf("two current builds agreed on v%d, want v%d", hs.agreed, versionCurrent)
	}
}

// The header travels in the clear, so it can be rewritten. The only thing that
// achieves is a downgrade, and the reply is authenticated — so the dialler can
// see that what arrived is not what it sent, and refuse.
func TestARewrittenVersionInTheHeaderIsCaught(t *testing.T) {
	const token = "a-version-negotiation-token-0123"

	hs, err := beginHandshake(token, 0, "ipip")
	if err != nil {
		t.Fatalf("beginHandshake: %v", err)
	}
	h, body, err := parseHeader(hs.datagram())
	if err != nil {
		t.Fatalf("parseHeader: %v", err)
	}

	// Somebody on the path rewrites the announcement to a lower version. The
	// listener answers honestly about what it saw.
	const tampered = versionCurrent - 1
	_, reply, err := respondV(token, h.session, tampered, body, "ipip")
	if err != nil {
		t.Fatalf("respondV: %v", err)
	}
	_, rbody, err := parseHeader(reply)
	if err != nil {
		t.Fatalf("parseHeader(reply): %v", err)
	}

	_, err = hs.complete(rbody)
	if tampered > versionLegacy {
		if err == nil {
			t.Fatal("a rewritten version announcement was accepted")
		}
		if !strings.Contains(err.Error(), "saw v") {
			t.Errorf("the refusal does not explain the mismatch: %v", err)
		}
	}
}

// The encapsulation check still works through the new payload shape. A tunnel
// whose two ends wrap packets differently comes up perfectly and moves nothing,
// which is the failure this check was added for.
func TestTheEncapsulationMismatchSurvivesTheVersionBlock(t *testing.T) {
	const token = "a-version-negotiation-token-0123"

	hs, err := beginHandshake(token, 0, "ipip")
	if err != nil {
		t.Fatalf("beginHandshake: %v", err)
	}
	h, body, err := parseHeader(hs.datagram())
	if err != nil {
		t.Fatalf("parseHeader: %v", err)
	}

	// The listener wraps packets differently. It still answers — the peer holds
	// the token, so it is the other half of a misconfigured tunnel rather than
	// a stranger — and the dialler must refuse by name.
	_, reply, respErr := respondV(token, h.session, int(h.counter), body, "gre")
	if respErr == nil {
		t.Fatal("the listener accepted a dialler that wraps packets differently")
	}
	_, rbody, err := parseHeader(reply)
	if err != nil {
		t.Fatalf("parseHeader(reply): %v", err)
	}
	_, err = hs.complete(rbody)
	if err == nil {
		t.Fatal("the dialler accepted a reply from a peer using a different encapsulation")
	}
	if !strings.Contains(err.Error(), "wrap packets differently") {
		t.Errorf("the refusal is not the encapsulation one: %v", err)
	}
}

// The parser has to tell an old payload from a new one without ambiguity, and
// refuse a malformed one rather than guessing.
func TestTheReplyPayloadParser(t *testing.T) {
	for _, tc := range []struct {
		in          string
		encap       string
		theirs, saw int
		wantErr     bool
		why         string
	}{
		{in: "ipip", encap: "ipip", theirs: 0, saw: 0, why: "an old peer's reply"},
		{in: "", encap: "", theirs: 0, saw: 0, why: "a peer older than the encap check"},
		{in: "gre:7", encap: "gre:7", theirs: 0, saw: 0, why: "a keyed GRE identifier has a colon but no NUL"},
		{in: "ipip\x00v1,1", encap: "ipip", theirs: 1, saw: 1, why: "both ends current"},
		{in: "gre\x00v2,1", encap: "gre", theirs: 2, saw: 1, why: "a peer newer than this build"},
		{in: "ipip\x00garbage", wantErr: true, why: "a block this build cannot read"},
		{in: "ipip\x00v1", wantErr: true, why: "an incomplete block"},
		{in: "ipip\x00vx,y", wantErr: true, why: "not numbers"},
	} {
		encap, theirs, saw, err := parseReplyPayload(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseReplyPayload(%q) was accepted — %s", tc.in, tc.why)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseReplyPayload(%q) failed: %v — %s", tc.in, err, tc.why)
			continue
		}
		if encap != tc.encap || theirs != tc.theirs || saw != tc.saw {
			t.Errorf("parseReplyPayload(%q) = %q/%d/%d, want %q/%d/%d — %s",
				tc.in, encap, theirs, saw, tc.encap, tc.theirs, tc.saw, tc.why)
		}
	}
}

// Agreeing means taking the lower of the two, so a newer peer does not speak
// something this build cannot read.
func TestTheAgreedVersionIsTheLowerOfTheTwo(t *testing.T) {
	for _, tc := range []struct{ mine, theirs, want int }{
		{1, 1, 1}, {1, 0, 0}, {0, 1, 0}, {1, 5, 1}, {5, 1, 1},
	} {
		if got := agreedVersion(tc.mine, tc.theirs); got != tc.want {
			t.Errorf("agreedVersion(%d, %d) = %d, want %d", tc.mine, tc.theirs, got, tc.want)
		}
	}
}
