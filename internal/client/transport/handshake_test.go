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

func TestControlSignalPrefersV2UntilTheServerProvesOtherwise(t *testing.T) {
	var p legacyProbe
	if got := p.signal(); got != utils.SG_ChanV2 {
		t.Fatalf("first attempt asks for signal %d, want SG_ChanV2", got)
	}
	p.legacy.Store(true)
	if got := p.signal(); got != utils.SG_Chan {
		t.Fatalf("after falling back, attempt asks for signal %d, want SG_Chan", got)
	}
}

// The fallback is only about a server that does not know the new signal, so it
// must not be triggered by a legacy attempt failing for some other reason.
func TestMissOnlyFlipsAfterRepeatedV2Attempts(t *testing.T) {
	var p legacyProbe

	for i := 0; i < legacyMissThreshold+1; i++ {
		p.miss(silentLogger(), utils.SG_Chan)
	}
	if p.legacy.Load() {
		t.Fatal("failed legacy attempts flipped the fallback")
	}

	p.miss(silentLogger(), utils.SG_ChanV2)
	if p.legacy.Load() {
		t.Fatalf("a single unanswered v2 attempt flipped the fallback; it takes %d", legacyMissThreshold)
	}
	for i := 1; i < legacyMissThreshold; i++ {
		p.miss(silentLogger(), utils.SG_ChanV2)
	}
	if !p.legacy.Load() {
		t.Fatalf("%d unanswered v2 attempts did not flip the fallback", legacyMissThreshold)
	}
}

// The reported failure: a server answered a v2 handshake, the path died
// thirteen minutes later, and the EOF from reconnecting into the still-broken
// path was read as proof the server was old. A server does not become old.
func TestMissIsIgnoredOnceTheServerHasAnsweredV2(t *testing.T) {
	var p legacyProbe
	p.ack(utils.SG_ChanV2)

	for i := 0; i < legacyMissThreshold*3; i++ {
		p.miss(silentLogger(), utils.SG_ChanV2)
	}
	if p.legacy.Load() {
		t.Fatal("a server that had already answered v2 was declared old on later failures")
	}
	if got := p.signal(); got != utils.SG_ChanV2 {
		t.Fatalf("still asking for signal %d after the failures, want SG_ChanV2", got)
	}
}

// A successful v2 answer clears a fallback that was reached by guessing, so a
// client does not stay degraded once the server has spoken for itself.
func TestAckClearsTheFallback(t *testing.T) {
	var p legacyProbe
	for i := 0; i < legacyMissThreshold; i++ {
		p.miss(silentLogger(), utils.SG_ChanV2)
	}
	if !p.legacy.Load() {
		t.Fatal("setup: the fallback did not latch")
	}

	p.ack(utils.SG_ChanV2)
	if p.legacy.Load() {
		t.Fatal("a v2 answer did not clear the fallback")
	}
}

// A legacy answer is not proof of anything about v2, so it must not arm the
// guard that suppresses the fallback.
func TestLegacyAckDoesNotCountAsProof(t *testing.T) {
	var p legacyProbe
	p.ack(utils.SG_Chan)

	for i := 0; i < legacyMissThreshold; i++ {
		p.miss(silentLogger(), utils.SG_ChanV2)
	}
	if !p.legacy.Load() {
		t.Fatal("a legacy answer suppressed the fallback")
	}
}

// The fallback used to be a one-way latch nothing cleared, so one dropped
// connection cost the nonce for the life of the process.
func TestResetReArmsTheProbe(t *testing.T) {
	var p legacyProbe
	for i := 0; i < legacyMissThreshold; i++ {
		p.miss(silentLogger(), utils.SG_ChanV2)
	}
	if !p.legacy.Load() {
		t.Fatal("setup: the fallback did not latch")
	}

	p.reset()
	if p.legacy.Load() {
		t.Fatal("reset left the fallback latched")
	}
	if got := p.signal(); got != utils.SG_ChanV2 {
		t.Fatalf("after reset the next attempt asks for signal %d, want SG_ChanV2", got)
	}
	// The counter has to go with it, or one more failure re-latches at once.
	p.miss(silentLogger(), utils.SG_ChanV2)
	if p.legacy.Load() {
		t.Fatal("reset kept the miss count, so a single failure re-latched")
	}
}

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
