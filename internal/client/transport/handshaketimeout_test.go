package transport

import (
	"testing"
	"time"
)

// The client's half of the same budget. See the server package's test of
// controlClaimTimeout for the failure this guards against.

const tcpRetransmitBudget = 1*time.Second + 2*time.Second

func TestTheClientWaitsLongEnoughForALossyHandshake(t *testing.T) {
	if controlAckTimeout <= tcpRetransmitBudget {
		t.Errorf("the client allows %s for the server's answer, which does not cover "+
			"the %s two TCP retransmissions take — one lost packet fails the "+
			"handshake and the tunnel flaps",
			controlAckTimeout, tcpRetransmitBudget)
	}
}
