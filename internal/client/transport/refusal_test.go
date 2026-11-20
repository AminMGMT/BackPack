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

// Refusals and the fallback: see peerauth_test.go.
