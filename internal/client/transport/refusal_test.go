package transport

import (
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/utils"
)

// Three different faults used to reach the operator as one symptom.
//
// The server closed the connection on a token it did not recognise, and on a
// control channel it had already given to somebody else. Both arrive at the
// client as
//
//	failed to read message length from net.Conn: EOF
//
// which is also exactly what a server too old to understand the signal
// produces. The client could not tell them apart and guessed the least likely
// of the three, so a report came in of tunnels that would not come up while the
// log insisted the far server needed upgrading — and it did not. It had the
// wrong token.
//
// The fix is that the server says which it is. These check the client can read
// that and does not mistake it for anything else.
func TestARefusalIsReadBackAsItsReason(t *testing.T) {
	for _, tc := range []struct {
		reason string
		expect string
	}{
		{utils.RefusedBadToken, "token"},
		{utils.RefusedInUse, "already has a control channel"},
		{"something new", "something new"},
	} {
		why, refused := refusalReason(tc.reason, utils.SG_Refused)
		if !refused {
			t.Fatalf("a refusal carrying %q was not recognised as one", tc.reason)
		}
		if !strings.Contains(why, tc.expect) {
			t.Errorf("a %q refusal reads as %q, which does not mention %q", tc.reason, why, tc.expect)
		}
	}
}

// An ordinary answer must not be mistaken for a refusal, or every working
// tunnel reports one.
func TestAnOrdinaryAnswerIsNotARefusal(t *testing.T) {
	for _, sig := range []byte{utils.SG_Chan, utils.SG_ChanV2} {
		if why, refused := refusalReason("a-token", sig); refused {
			t.Errorf("an answer on signal %d was read as a refusal: %q", sig, why)
		}
	}
}

// A refusal is proof the server understood the handshake and declined it, so it
// must not count towards deciding the server is old — which would drop the
// client to the legacy handshake for a fault the handshake has nothing to do
// with.
func TestARefusalDoesNotMakeTheServerLookOld(t *testing.T) {
	var p legacyProbe

	for i := 0; i < legacyMissThreshold*3; i++ {
		// What the dial loop does on a refusal.
		p.ack(utils.SG_Refused)
	}
	if p.legacy.Load() {
		t.Fatal("a refused handshake flipped the fallback; the server answered, it " +
			"simply said no")
	}
	if got := p.signal(); got != utils.SG_ChanV2 {
		t.Errorf("after refusals the client asks for signal %d, want SG_ChanV2", got)
	}
}

// And the reverse: a refusal must not be taken as proof the server speaks v2
// either, because it says nothing about that.
func TestARefusalIsNotProofOfV2(t *testing.T) {
	var p legacyProbe
	p.ack(utils.SG_Refused)

	for i := 0; i < legacyMissThreshold; i++ {
		p.miss(silentLogger(), utils.SG_ChanV2)
	}
	if !p.legacy.Load() {
		t.Error("a refusal was treated as proof the server understands v2, so a " +
			"genuinely old server can no longer be detected")
	}
}
