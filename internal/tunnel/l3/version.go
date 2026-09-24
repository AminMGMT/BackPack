package l3

import (
	"fmt"
	"strconv"
	"strings"
)

// Changing the wire without breaking every pair already running it.
//
// There was no way to. The handshake payload carries the encapsulation
// identifier and the peer compares it whole — `theirs != encap` is a mismatch —
// so adding a single field to that payload breaks a new dialler talking to an
// old listener. Every wire-affecting change was therefore gated behind a
// problem nobody had solved, which is why the handshake still has no freshness
// in it.
//
// It matters more here than in most protocols because of how an update lands.
// The two ends are two machines and are never updated at the same instant;
// there is always a window where one end is new and the other is not, and on
// these deployments that window can be weeks. A change that forgets it does not
// degrade — it takes the tunnel down until somebody updates the far side, over
// a link that may be the only way to reach it.
//
// # Where the version goes
//
// The dialler announces itself in the **header's counter field**, and this is
// the whole trick: on a handshake message that field is unused. typeInit and
// typeResp both carry counter 0 today, parseHeader validates only the kind, and
// neither respond() nor handleInit ever looks at it. An old peer therefore
// ignores whatever is put there, which makes it the one place a new dialler can
// speak without an old listener noticing.
//
// The listener answers in the **reply payload**, which is inside the Noise
// encryption and authenticated with it. It appends its own version and, with
// it, the version it saw — so a new dialler can check that what arrived is what
// it sent.
//
//	old dialler -> new listener   counter 0        -> treated as v0, old reply
//	new dialler -> old listener   counter ignored  -> old reply, dialler falls back
//	new dialler -> new listener   counter v1       -> versioned reply, both agree
//
// # What the check in the reply is for
//
// The counter travels in the clear: the header of a handshake message is not
// covered by any AEAD, because there are no keys yet. So an on-path attacker
// can rewrite it, and the only thing they can achieve by doing so is a
// downgrade to v0 — which is exactly where this protocol already is, so it is
// not a new weakness. It is still worth detecting, because a downgrade stops
// being harmless the moment v1 carries something security-relevant.
//
// Hence the echo. The listener puts the version it *saw* into the reply, the
// reply is authenticated, and the dialler refuses when it does not match what
// it sent. That is the same shape as TLS's downgrade protection and it costs
// two integers.

// Protocol versions of the layer-3 handshake.
const (
	// versionLegacy is every build before this negotiation existed. It is what
	// a peer is assumed to be when it says nothing.
	versionLegacy = 0

	// version1 adds nothing on its own. It exists so that the *next* wire
	// change has somewhere to be announced — building the mechanism before it
	// is needed is the entire point, because building it during is what is
	// impossible.
	version1 = 1

	// version2 puts a monotonic timestamp inside the dialler's encrypted
	// handshake payload, and a listener that speaks it refuses one that does
	// not advance. See freshness.go.
	version2 = 2

	// versionCurrent is what this build announces and the highest it
	// understands.
	versionCurrent = version2
)

// versionSep separates the encapsulation identifier from the version block in
// a reply payload. A NUL, because an encapsulation identifier is "ipip",
// "gre" or "gre:<number>" and can never contain one — so an old-format payload
// and a new one are told apart without ambiguity.
const versionSep = "\x00"

// replyPayload renders the listener's answer: its encapsulation, and — when the
// dialler announced a version — the version block.
//
// saw is the version the listener read out of the dialler's header. Echoing it
// is what lets the dialler notice a header that was rewritten in flight.
// The parameter order matches what parseReplyPayload hands back — mine first,
// then what was seen — on purpose. It used to be the other way round, and the
// two being mirror images is a trap somebody falls into exactly once per
// reading of this file: the writer took (saw, mine) while the reader returned
// (theirs, sawMine), which are the same two numbers in the opposite order and
// are both plain ints, so swapping them compiles and produces a tunnel that
// negotiates the wrong version in one direction only.
func replyPayload(encap string, mine, saw int) string {
	if saw <= versionLegacy {
		// The dialler said nothing, so it is an old build and the reply has to
		// be the shape it expects: the encapsulation and nothing else.
		return encap
	}
	return replyPayloadBlock(encap, mine, saw)
}

// replyPayloadBlock is the reply with its version block, even for a header
// that announced nothing. See respondFresh for when that is the right answer.
func replyPayloadBlock(encap string, mine, saw int) string {
	return encap + versionSep + "v" + strconv.Itoa(mine) + "," + strconv.Itoa(max(saw, 0))
}

// parseReplyPayload splits a reply into the encapsulation and what the peer
// said about versions. A payload with no version block is an old peer.
func parseReplyPayload(payload string) (encap string, theirs, sawMine int, err error) {
	encap, rest, found := strings.Cut(payload, versionSep)
	if !found {
		return encap, versionLegacy, versionLegacy, nil
	}
	if !strings.HasPrefix(rest, "v") {
		return "", 0, 0, fmt.Errorf("l3: the peer's reply has a version block this build cannot read")
	}
	mine, saw, ok := strings.Cut(strings.TrimPrefix(rest, "v"), ",")
	if !ok {
		return "", 0, 0, fmt.Errorf("l3: the peer's version block is incomplete")
	}
	theirs, err1 := strconv.Atoi(mine)
	sawMine, err2 := strconv.Atoi(saw)
	if err1 != nil || err2 != nil {
		return "", 0, 0, fmt.Errorf("l3: the peer's version block is not a pair of numbers")
	}
	// A version is a count, so a negative one is not a version at all.
	//
	// Atoi is happy with "-1", and a negative number travels straight through
	// agreedVersion — which takes the lower of the two — so a peer claiming
	// v-1 negotiated the session down to a version that does not exist. It
	// then behaves as legacy, which is the one outcome the negotiation was
	// built to make impossible, and the downgrade check does not catch it
	// because that check looks at the echo of *our* announcement rather than
	// at the sanity of theirs.
	//
	// Found by FuzzParseReplyPayload on its first seed.
	if theirs < 0 || sawMine < 0 {
		return "", 0, 0, fmt.Errorf("l3: the peer announced a negative protocol version")
	}
	return encap, theirs, sawMine, nil
}

// agreedVersion is the highest both ends understand.
func agreedVersion(mine, theirs int) int {
	if theirs < mine {
		return theirs
	}
	return mine
}

// errDowngraded is what a rewritten header looks like from the dialler's side.
func errDowngraded(sent, seen int) error {
	return fmt.Errorf("l3: this end announced protocol v%d and the peer says it saw v%d. "+
		"The version travels in the clear in the handshake header, so something on the "+
		"path changed it — which can only weaken what the two ends agree on, never "+
		"strengthen it. Refusing rather than continuing on the weaker terms", sent, seen)
}
