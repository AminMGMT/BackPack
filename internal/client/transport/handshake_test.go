package transport

import (
	"io"
	"testing"

	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"

	"github.com/sirupsen/logrus"
)

func silentLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

// The fallback that replaced legacyProbe is network.PeerGuard; its tests are
// in internal/utils/network/peerauth_test.go.

func TestDecodeControlAck(t *testing.T) {
	nonce, err := network.NewPoolNonce()
	if err != nil {
		t.Fatalf("NewPoolNonce: %v", err)
	}

	// A legacy server answers with the bare token, no nonce and no version —
	// there is nothing for the client to adopt, so it keeps its own settings.
	token, got, version := decodeControlAck("a-token", utils.SG_Chan)
	if token != "a-token" || got != "" || version != 0 {
		t.Fatalf("legacy answer decoded as (%q, %q, %d), want (%q, \"\", 0)", token, got, version, "a-token")
	}

	// A current server answers with all three.
	token, got, version = decodeControlAck(network.EncodeControlAck("a-token", nonce, 2), utils.SG_ChanV2)
	if token != "a-token" {
		t.Fatalf("v2 answer decoded token %q, want %q", token, "a-token")
	}
	if got != nonce {
		t.Fatalf("v2 answer decoded nonce %q, want %q", got, nonce)
	}
	if version != 2 {
		t.Fatalf("v2 answer decoded mux version %d, want 2", version)
	}

	// Something else entirely must not produce a usable token.
	if token, _, _ := decodeControlAck("junk", utils.SG_ChanV2); token == "a-token" {
		t.Fatal("a malformed v2 answer decoded to the expected token")
	}
}

// Against a legacy server there is no nonce, and a pool connection must then
// send nothing at all — sending would leave bytes the server never reads,
// which it would go on to treat as tunnel payload.
func TestAnnouncePoolConnSendsNothingWithoutANonce(t *testing.T) {
	if err := announcePoolConn(nil, ""); err != nil {
		t.Fatalf("announcePoolConn with no nonce: %v", err)
	}
}
