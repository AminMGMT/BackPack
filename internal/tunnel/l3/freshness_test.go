package l3

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestTheInitPayloadRoundTrips(t *testing.T) {
	for _, tc := range []struct {
		encap string
		fresh uint64
	}{{"gre", 0}, {"gre", 1}, {"ipip", 1<<63 + 5}, {"", 42}, {"gre:7", 1727}} {
		encap, fresh, err := parseInitPayload(initPayload(tc.encap, tc.fresh))
		if err != nil || encap != tc.encap || fresh != tc.fresh {
			t.Errorf("%q/%d came back as %q/%d (%v)", tc.encap, tc.fresh, encap, fresh, err)
		}
	}
	// What every build before v2 sends is still read, as a legacy payload.
	if encap, fresh, err := parseInitPayload("gre"); err != nil || encap != "gre" || fresh != 0 {
		t.Errorf("a legacy payload read as %q/%d (%v)", encap, fresh, err)
	}
	for _, bad := range []string{"gre" + freshSep, "gre" + freshSep + "x", "gre" + freshSep + "0"} {
		if _, _, err := parseInitPayload(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestTheDiallersClockOnlyRises(t *testing.T) {
	var c freshClock
	now := time.Now()
	a := c.next(now)
	b := c.next(now)                 // the same instant
	d := c.next(now.Add(-time.Hour)) // the clock stepped back
	if !(a < b && b < d) {
		t.Fatalf("timestamps %d, %d, %d do not rise", a, b, d)
	}
}

func TestTheListenerRefusesATimestampThatDoesNotAdvance(t *testing.T) {
	var j freshJudge
	if err := j.admit(0); err != nil {
		t.Fatalf("a legacy handshake before any timestamp was refused: %v", err)
	}
	if err := j.admit(100); err != nil {
		t.Fatal(err)
	}
	for _, replay := range []uint64{100, 99, 1} {
		if err := j.admit(replay); err == nil {
			t.Errorf("timestamp %d after 100 was admitted", replay)
		}
	}
	if err := j.admit(101); err != nil {
		t.Fatalf("a newer timestamp was refused: %v", err)
	}
	// Once the dialler has shown it sends timestamps, a handshake without one
	// is a replay from before its upgrade.
	if err := j.admit(0); err == nil {
		t.Error("a legacy handshake was admitted after timestamped ones")
	}
}

// legacyResponderReply answers exactly as every listener before v2 did: it
// reads the payload, compares it whole with its own encapsulation, and on a
// mismatch still replies — with the bare encapsulation — so the dialler can
// say what is wrong. Copied in behaviour from v1.8.1's respond.
func legacyResponderReply(t *testing.T, token string, msg []byte, encap string) (reply []byte, refused bool) {
	t.Helper()
	state, err := newHandshakeState(token, false)
	if err != nil {
		t.Fatal(err)
	}
	payload, _, _, err := state.ReadMessage(nil, msg)
	if err != nil {
		t.Fatal(err)
	}
	reply, _, _, err = state.WriteMessage(nil, []byte(encap))
	if err != nil {
		t.Fatal(err)
	}
	return reply, string(payload) != "" && string(payload) != encap
}

// A timestamped attempt against an old listener is refused by it, and the
// dialler hears that as "fall back", not as a working session.
func TestATimestampedHandshakeMeetsAnOldListener(t *testing.T) {
	const token = "a-freshness-token"
	attempt, err := beginHandshakeFresh(token, 0, "gre", 12345)
	if err != nil {
		t.Fatal(err)
	}
	reply, refused := legacyResponderReply(t, token, attempt.msg, "gre")
	if !refused {
		t.Fatal("the legacy listener accepted a timestamped payload; the test is not modelling it")
	}
	if _, err := attempt.complete(reply); !errors.Is(err, errPeerLegacy) {
		t.Fatalf("complete = %v, want errPeerLegacy", err)
	}

	// And the legacy payload it falls back to is one the old listener takes.
	legacy, err := beginHandshakeFresh(token, 0, "gre", 0)
	if err != nil {
		t.Fatal(err)
	}
	reply, refused = legacyResponderReply(t, token, legacy.msg, "gre")
	if refused {
		t.Fatal("the old listener refused the legacy payload")
	}
	if _, err := legacy.complete(reply); err != nil {
		t.Fatalf("the legacy handshake did not complete: %v", err)
	}
}

// Between two v2 builds the timestamp arrives, authenticated, at the listener.
func TestAV2ListenerReadsTheTimestamp(t *testing.T) {
	const token = "a-freshness-token"
	attempt, err := beginHandshakeFresh(token, 0, "gre", 777)
	if err != nil {
		t.Fatal(err)
	}
	_, reply, fresh, err := respondFresh(token, attempt.id, versionCurrent, attempt.msg, "gre")
	if err != nil || fresh != 777 {
		t.Fatalf("respondFresh = %d, %v", fresh, err)
	}
	if _, err := attempt.complete(reply[headerLen:]); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

// The attack itself: a handshake recorded off the wire and replayed later is
// refused by a listener that has seen a newer one, and cannot displace the
// real dialler's session.
func TestARecordedHandshakeIsRefusedOnceANewerOneIsSeen(t *testing.T) {
	p := established(t, "gre", 0)
	across(t, p.dialDev, p.listenDev, ipv4Packet(1))

	// Record what an eavesdropper would have: a genuine init from this
	// dialler, sent now...
	p.dialer.mu.Lock()
	ts := p.dialer.freshClock.next(time.Now())
	p.dialer.mu.Unlock()
	old, err := beginHandshakeFresh(p.dialer.cfg.Token, 0, encapID(p.dialer.encap), ts)
	if err != nil {
		t.Fatal(err)
	}
	recorded := old.datagram()
	from := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 7), Port: 4444}

	// ...which the listener takes once, as it would have when it was live.
	p.listener.handleInit(parseHeaderT(t, recorded), recorded[headerLen:], from)

	// The dialler then rekeys, as it does every two minutes: a newer stamp.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.dialer.negotiate(ctx); err != nil {
		t.Fatalf("rekey: %v", err)
	}
	across(t, p.dialDev, p.listenDev, ipv4Packet(2))

	// Replayed now — with a new session id, as an attacker would, since the
	// id is not authenticated and the ten-minute memory keys on it.
	h := parseHeaderT(t, recorded)
	h.session ^= 0x5a5a5a5a
	p.listener.mu.RLock()
	pendingBefore := p.listener.pending
	p.listener.mu.RUnlock()
	p.listener.handleInit(h, recorded[headerLen:], from)
	p.listener.mu.RLock()
	pendingAfter := p.listener.pending
	p.listener.mu.RUnlock()
	if pendingAfter != pendingBefore {
		t.Fatal("a replayed handshake became the pending session")
	}
	across(t, p.dialDev, p.listenDev, ipv4Packet(3))
}

func parseHeaderT(t *testing.T, datagram []byte) header {
	t.Helper()
	h, _, err := parseHeader(datagram)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// The header is not authenticated. A dialler's v2 handshake whose version was
// rewritten to 0 in transit must not come back looking like an old listener's
// answer — that was a one-byte way to force the replayable handshake for half
// an hour. It comes back as a downgrade, which the dialler refuses.
func TestARewrittenHeaderCannotForceTheLegacyHandshake(t *testing.T) {
	const token = "a-freshness-token"
	attempt, err := beginHandshakeFresh(token, 0, "gre", 4242)
	if err != nil {
		t.Fatal(err)
	}
	// What arrives: the same encrypted payload, the header claiming v0.
	_, reply, fresh, err := respondFresh(token, attempt.id, versionLegacy, attempt.msg, "gre")
	if err != nil || fresh != 4242 {
		t.Fatalf("respondFresh = %d, %v", fresh, err)
	}
	_, err = attempt.complete(reply[headerLen:])
	if errors.Is(err, errPeerLegacy) {
		t.Fatal("a flipped header byte made the dialler fall back to the legacy handshake")
	}
	if err == nil || !strings.Contains(err.Error(), "announced protocol v2") {
		t.Fatalf("complete = %v, want it refused as a downgrade", err)
	}
}

// Field report on v1.8.3: "a direct tunnel stops working after a while".
//
// The dialler stamps each handshake with its wall clock, and the listener
// refuses one that is not newer than the last it accepted. A dialler whose
// clock stepped back — NTP correcting a clock that ran fast — and that then
// restarts (the watchdog, Auto Refresh, a reboot) sends stamps older than what
// the listener remembers, and every one of them was refused as a replay: the
// tunnel stayed down until the clock caught up, which can be hours, or until
// somebody restarted the kharej. Once the listener has no session left, the
// refusals are what is keeping the tunnel down, and after a grace it takes the
// dialler's clock as it now is.
func TestADiallerWhoseClockWentBackReconnects(t *testing.T) {
	defer func(g time.Duration) { clockStepGrace = g }(clockStepGrace)
	clockStepGrace = 300 * time.Millisecond

	p := established(t, "gre", 0)
	across(t, p.dialDev, p.listenDev, ipv4Packet(1))

	// The listener remembers a stamp an hour ahead of the dialler's clock now:
	// the dialler's clock was fast, and has been set back.
	p.listener.mu.Lock()
	p.listener.fresh.last = uint64(time.Now().Add(time.Hour).UnixNano())
	// Its session from before the dialler restarted has expired.
	p.listener.current, p.listener.previous, p.listener.pending = nil, nil, nil
	p.listener.mu.Unlock()
	// The dialler restarted: a fresh process, a fresh clock.
	p.dialer.mu.Lock()
	p.dialer.freshClock = freshClock{}
	p.dialer.mu.Unlock()

	deadline := time.Now().Add(8 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := p.dialer.negotiate(ctx)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the dialler never got back in once its clock went back: %v", err)
		}
	}
	across(t, p.dialDev, p.listenDev, ipv4Packet(2))
}

// The grace is not a hole: while the listener still holds a working session,
// a stamp older than the last one accepted is refused however long it has
// been refused for — which is what makes a recorded handshake worthless.
func TestAStaleStampIsRefusedWhileASessionIsUp(t *testing.T) {
	defer func(g time.Duration) { clockStepGrace = g }(clockStepGrace)
	clockStepGrace = 0

	p := established(t, "gre", 0)
	p.dialer.mu.Lock()
	ts := p.dialer.freshClock.next(time.Now())
	p.dialer.mu.Unlock()
	old, err := beginHandshakeFresh(p.dialer.cfg.Token, 0, encapID(p.dialer.encap), ts)
	if err != nil {
		t.Fatal(err)
	}
	recorded := old.datagram()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.dialer.negotiate(ctx); err != nil {
		t.Fatalf("rekey: %v", err)
	}
	across(t, p.dialDev, p.listenDev, ipv4Packet(1))

	from := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 7), Port: 4444}
	h := parseHeaderT(t, recorded)
	h.session ^= 0x5a5a5a5a
	for range 3 {
		p.listener.mu.RLock()
		before := p.listener.pending
		p.listener.mu.RUnlock()
		p.listener.handleInit(h, recorded[headerLen:], from)
		p.listener.mu.RLock()
		after := p.listener.pending
		p.listener.mu.RUnlock()
		if after != before {
			t.Fatal("a replayed handshake became the pending session while a session was up")
		}
		h.session++
	}
}
