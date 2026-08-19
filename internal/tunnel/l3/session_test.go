package l3

import (
	"bytes"
	"testing"
	"time"
)

// handshakePair runs a full handshake and returns the two completed sessions.
func handshakePair(t *testing.T, token string) (initiator, responder *session) {
	t.Helper()
	hs, err := beginHandshake(token, 0, "ipip")
	if err != nil {
		t.Fatalf("beginHandshake: %v", err)
	}
	first := hs.datagram()
	h, body, err := parseHeader(first)
	if err != nil {
		t.Fatalf("parseHeader(init): %v", err)
	}
	if h.kind != typeInit || h.session != hs.id {
		t.Fatalf("init header = %+v, want kind %d session %d", h, typeInit, hs.id)
	}

	resp, reply, err := respond(token, h.session, body, "ipip")
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	rh, rbody, err := parseHeader(reply)
	if err != nil {
		t.Fatalf("parseHeader(resp): %v", err)
	}
	if rh.kind != typeResp || rh.session != hs.id {
		t.Fatalf("resp header = %+v, want kind %d session %d", rh, typeResp, hs.id)
	}

	init, err := hs.complete(rbody)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	return init, resp
}

func TestSessionRoundTripBothDirections(t *testing.T) {
	init, resp := handshakePair(t, "a-shared-tunnel-token")

	// Initiator to responder.
	payload := []byte("the quick brown fox jumps over the lazy dog")
	sealed, err := init.seal(nil, payload)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	h, body, err := parseHeader(sealed)
	if err != nil {
		t.Fatalf("parseHeader: %v", err)
	}
	got, err := resp.open(nil, h, body)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round trip = %q, want %q", got, payload)
	}

	// Responder back to initiator, which uses the other key entirely.
	back := []byte("and the dog said nothing at all")
	sealed, err = resp.seal(nil, back)
	if err != nil {
		t.Fatalf("seal (reverse): %v", err)
	}
	h, body, err = parseHeader(sealed)
	if err != nil {
		t.Fatalf("parseHeader (reverse): %v", err)
	}
	got, err = init.open(nil, h, body)
	if err != nil {
		t.Fatalf("open (reverse): %v", err)
	}
	if !bytes.Equal(got, back) {
		t.Fatalf("reverse round trip = %q, want %q", got, back)
	}
}

func TestSessionWrongTokenIsRejected(t *testing.T) {
	hs, err := beginHandshake("the-right-token", 0, "ipip")
	if err != nil {
		t.Fatalf("beginHandshake: %v", err)
	}
	_, body, err := parseHeader(hs.datagram())
	if err != nil {
		t.Fatalf("parseHeader: %v", err)
	}
	if _, _, err := respond("a-different-token", hs.id, body, "ipip"); err == nil {
		t.Fatal("respond accepted a handshake made with a different token")
	}
}

// A packet must not decrypt under a session it does not belong to, even when
// both were made from the same token.
func TestSessionsAreNotInterchangeable(t *testing.T) {
	initA, _ := handshakePair(t, "shared-token")
	_, respB := handshakePair(t, "shared-token")

	sealed, err := initA.seal(nil, []byte("for session A only"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	h, body, err := parseHeader(sealed)
	if err != nil {
		t.Fatalf("parseHeader: %v", err)
	}
	if _, err := respB.open(nil, h, body); err == nil {
		t.Fatal("a packet from one session opened under another")
	}
}

func TestSessionReplayIsRejected(t *testing.T) {
	init, resp := handshakePair(t, "token")

	sealed, err := init.seal(nil, []byte("exactly once"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// The datagram is captured before the first open, because open decrypts in
	// place into its own buffer but the body still aliases this slice.
	captured := append([]byte(nil), sealed...)

	h, body, err := parseHeader(captured)
	if err != nil {
		t.Fatalf("parseHeader: %v", err)
	}
	if _, err := resp.open(nil, h, body); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	h, body, err = parseHeader(captured)
	if err != nil {
		t.Fatalf("parseHeader (replay): %v", err)
	}
	if _, err := resp.open(nil, h, body); err == nil {
		t.Fatal("a replayed datagram was accepted")
	}
}

// Datagrams reorder on any real path; the session has to take them in any
// order and still reject a genuine duplicate.
func TestSessionAcceptsReorderedDelivery(t *testing.T) {
	init, resp := handshakePair(t, "token")

	const count = 64
	datagrams := make([][]byte, count)
	for i := range datagrams {
		sealed, err := init.seal(nil, []byte{byte(i), 4, 5, 6})
		if err != nil {
			t.Fatalf("seal %d: %v", i, err)
		}
		datagrams[i] = append([]byte(nil), sealed...)
	}

	// Deliver back to front.
	for i := count - 1; i >= 0; i-- {
		h, body, err := parseHeader(datagrams[i])
		if err != nil {
			t.Fatalf("parseHeader %d: %v", i, err)
		}
		got, err := resp.open(nil, h, body)
		if err != nil {
			t.Fatalf("out-of-order delivery of %d: %v", i, err)
		}
		if got[0] != byte(i) {
			t.Fatalf("datagram %d carried %d", i, got[0])
		}
	}

	// Every one of them is now a replay.
	for i := range datagrams {
		h, body, err := parseHeader(datagrams[i])
		if err != nil {
			t.Fatalf("parseHeader %d: %v", i, err)
		}
		if _, err := resp.open(nil, h, body); err == nil {
			t.Fatalf("replay of %d was accepted", i)
		}
	}
}

// Loss must not desynchronise anything: the nonce is explicit, so a gap in the
// counters is simply a gap.
func TestSessionSurvivesLoss(t *testing.T) {
	init, resp := handshakePair(t, "token")

	for i := 0; i < 100; i++ {
		sealed, err := init.seal(nil, []byte{byte(i)})
		if err != nil {
			t.Fatalf("seal %d: %v", i, err)
		}
		if i%3 != 0 {
			continue // dropped in flight
		}
		h, body, err := parseHeader(sealed)
		if err != nil {
			t.Fatalf("parseHeader %d: %v", i, err)
		}
		got, err := resp.open(nil, h, body)
		if err != nil {
			t.Fatalf("delivery of %d after losses: %v", i, err)
		}
		if got[0] != byte(i) {
			t.Fatalf("datagram %d carried %d", i, got[0])
		}
	}
}

// The header is additional data, so none of its fields can be edited in
// flight.
func TestSessionHeaderIsAuthenticated(t *testing.T) {
	init, resp := handshakePair(t, "token")
	sealed, err := init.seal(nil, []byte("intact"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	tampered := append([]byte(nil), sealed...)
	tampered[6] ^= 0x01 // a bit in the counter field

	h, body, err := parseHeader(tampered)
	if err != nil {
		t.Fatalf("parseHeader: %v", err)
	}
	if _, err := resp.open(nil, h, body); err == nil {
		t.Fatal("a datagram with an edited header authenticated")
	}
}

func TestSessionRekeySchedule(t *testing.T) {
	init, _ := handshakePair(t, "token")

	if init.dueForRekey(time.Now()) {
		t.Fatal("a fresh session already wants a rekey")
	}
	if init.expired(time.Now()) {
		t.Fatal("a fresh session is already expired")
	}
	if !init.dueForRekey(init.created.Add(rekeyAfterTime)) {
		t.Fatal("a session past rekeyAfterTime does not want a rekey")
	}
	if !init.expired(init.created.Add(rejectAfterTime)) {
		t.Fatal("a session past rejectAfterTime is not expired")
	}

	init.sendCounter.Store(rekeyAfterMessages)
	if !init.dueForRekey(time.Now()) {
		t.Fatal("a session at the message limit does not want a rekey")
	}
}

func TestNewSessionIDAvoidsZeroAndPredecessor(t *testing.T) {
	for i := 0; i < 200; i++ {
		id, err := newSessionID(7)
		if err != nil {
			t.Fatalf("newSessionID: %v", err)
		}
		if id == 0 || id == 7 {
			t.Fatalf("newSessionID returned %d", id)
		}
	}
}
