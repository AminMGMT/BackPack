package transport

import (
	"testing"
	"time"
)

// The handshake budget has to survive a lossy path, because a lossy path is the
// ordinary case for this tunnel rather than the exceptional one.
//
// This is the regression test for a tunnel that flapped under mild packet loss.
// The control-channel handshake is one exchange, but on a path that drops a
// packet the wait is not a round trip — it is a round trip plus however long
// TCP takes to retransmit. The first retransmission is a second out, the next
// three seconds, the next seven. The udp transport allowed two seconds for the
// whole thing, so a single loss in that exchange failed it: the client closed
// the connection, backed off and dialled again, and the tunnel churned without
// ever reporting a disconnection.

// tcpRetransmitBudget is how long two retransmissions take to land, from RFC
// 6298's one-second initial timer doubling each attempt.
const tcpRetransmitBudget = 1*time.Second + 2*time.Second

func TestTheServerWaitsLongEnoughForALossyHandshake(t *testing.T) {
	if controlClaimTimeout <= tcpRetransmitBudget {
		t.Errorf("the server allows %s for a peer to announce itself, which does not "+
			"cover the %s two TCP retransmissions take — one lost packet fails the "+
			"handshake and the tunnel flaps",
			controlClaimTimeout, tcpRetransmitBudget)
	}
}

// Failing fast buys nothing here: failing means backing off and dialling again,
// which costs far more than waiting would have. But it must still be bounded,
// or a silent peer holds its goroutine forever.
func TestTheServerHandshakeWaitIsStillBounded(t *testing.T) {
	if controlClaimTimeout > time.Minute {
		t.Errorf("controlClaimTimeout is %s; a peer that connects and says nothing "+
			"should not be held that long", controlClaimTimeout)
	}
}
