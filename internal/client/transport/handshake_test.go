package transport

import (
	"io"
	"sync/atomic"
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

func TestControlSignalPrefersV2UntilTheServerProvesOtherwise(t *testing.T) {
	var legacy atomic.Bool
	if got := controlSignal(&legacy); got != utils.SG_ChanV2 {
		t.Fatalf("first attempt asks for signal %d, want SG_ChanV2", got)
	}
	legacy.Store(true)
	if got := controlSignal(&legacy); got != utils.SG_Chan {
		t.Fatalf("after falling back, attempt asks for signal %d, want SG_Chan", got)
	}
}

// The fallback is only about a server that does not know the new signal, so it
// must not be triggered by a legacy attempt failing for some other reason.
func TestNoteLegacyServerOnlyFlipsAfterAV2Attempt(t *testing.T) {
	var legacy atomic.Bool

	noteLegacyServer(silentLogger(), &legacy, utils.SG_Chan)
	if legacy.Load() {
		t.Fatal("a failed legacy attempt flipped the fallback")
	}

	noteLegacyServer(silentLogger(), &legacy, utils.SG_ChanV2)
	if !legacy.Load() {
		t.Fatal("a failed v2 attempt did not flip the fallback")
	}
}

func TestDecodeControlAck(t *testing.T) {
	nonce, err := network.NewPoolNonce()
	if err != nil {
		t.Fatalf("NewPoolNonce: %v", err)
	}

	// A legacy server answers with the bare token and no nonce.
	token, got := decodeControlAck("a-token", utils.SG_Chan)
	if token != "a-token" || got != "" {
		t.Fatalf("legacy answer decoded as (%q, %q), want (%q, \"\")", token, got, "a-token")
	}

	// A current server answers with both.
	token, got = decodeControlAck(network.EncodeControlAck("a-token", nonce), utils.SG_ChanV2)
	if token != "a-token" {
		t.Fatalf("v2 answer decoded token %q, want %q", token, "a-token")
	}
	if got != nonce {
		t.Fatalf("v2 answer decoded nonce %q, want %q", got, nonce)
	}

	// Something else entirely must not produce a usable token.
	if token, _ := decodeControlAck("junk", utils.SG_ChanV2); token == "a-token" {
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
