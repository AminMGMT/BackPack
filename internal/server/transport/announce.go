package transport

import (
	"crypto/subtle"
	"net"
	"time"

	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"
)

// Reading a connection's announcement off the accept path.
//
// Every connection arriving on the tunnel port now says what it is before it is
// filed — a control channel offering the token, or a pool connection offering
// the nonce from the current run. Two things follow from that, and both are the
// point of doing it this way.
//
// The read happens in the connection's own goroutine. It used to happen in a
// single loop that took one connection at a time and blocked on it, which meant
// anything that connected and then said nothing cost every connection queued
// behind it the full timeout. On a public address that is not a rare event —
// port scanners and probes find an open port within hours — and the effect was
// that the tunnel's own client could not get a control channel established
// while a scanner was working through the port. A connection that says nothing
// now costs one goroutine and nothing else.
//
// And because a pool connection announces itself, the server can tell one from
// a stranger's by what it knows rather than by where it dialled from. See
// network.PoolNonce for why that matters.

// announceTimeout is how long a freshly accepted connection has to say what it
// is. It is generous — the client sends immediately, so this only has to cover
// a slow path, not a slow peer — because the cost of waiting is now one idle
// goroutine.
const announceTimeout = 10 * time.Second

// announcement is what a connection said about itself.
type announcement struct {
	// signal is SG_Chan or SG_ChanV2 for a control channel, SG_Pool for a pool
	// connection.
	signal byte
	// payload is the token for a control channel, the nonce for a pool one.
	payload string
}

// readAnnouncement reads one framed announcement under a deadline, clearing the
// deadline afterwards so the connection goes on to be used without one.
func readAnnouncement(conn net.Conn) (announcement, error) {
	if err := conn.SetReadDeadline(time.Now().Add(announceTimeout)); err != nil {
		return announcement{}, err
	}
	payload, signal, err := utils.ReceiveBinaryTransportString(conn)
	if err != nil {
		return announcement{}, err
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return announcement{}, err
	}
	return announcement{signal: signal, payload: payload}, nil
}

// isControlSignal reports whether a signal opens a control channel, in either
// the legacy or the nonce-carrying form.
func isControlSignal(sig byte) bool {
	return sig == utils.SG_Chan || sig == utils.SG_ChanV2
}

// tokenMatches compares an offered token to the configured one in constant
// time.
func tokenMatches(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// controlAck builds the answer to a control handshake and the nonce that goes
// with it. Answering with the token is what proves to the client that this
// server knows the secret too; a v2 handshake carries a fresh nonce alongside
// it, which is what the client's pool connections will present.
//
// A legacy handshake yields an empty nonce, which leaves pool authorisation off
// and the transport falling back to matching source addresses.
func controlAck(sig byte, token string) (ack, nonce string, err error) {
	if sig != utils.SG_ChanV2 {
		return token, "", nil
	}
	nonce, err = network.NewPoolNonce()
	if err != nil {
		return "", "", err
	}
	return network.EncodeControlAck(token, nonce), nonce, nil
}

// controlCandidate is a connection that has proved it holds the token and asked
// to be the control channel, waiting to be published as one.
type controlCandidate struct {
	conn net.Conn
	// nonce is what this run's pool connections must present, or "" when the
	// client spoke the legacy handshake and there is none.
	nonce string
}

// legacyPoolWarning is logged once per control channel established by a client
// too old to authorise its pool connections, because the resulting failure —
// a tunnel that connects and then carries nothing — says nothing about its own
// cause anywhere else.
const legacyPoolWarning = "control channel established by a legacy client: its pool connections are authorised by source address, which fails when the client dials out from more than one address (carrier-grade NAT, a multi-homed host, a SNAT pool). Upgrade the client to authorise them by token instead."
