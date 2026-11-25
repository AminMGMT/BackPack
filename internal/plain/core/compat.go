package core

import (
	"fmt"
	"net/http"
)

// Telling a Backpack v2.0.0 peer apart, so the tunnel can serve it on the v2.0.0
// engine until it is updated too (see internal/server/compat.go and
// internal/client/compat.go). Backpack's own signals, numbered after
// the plain engine's in v2.0.0's internal/utils/signals.go.
const (
	// SG_ChanV2 and SG_ChanAuth open a control channel the v2.0.0 way.
	SG_ChanV2   byte = 7
	SG_ChanAuth byte = 10
	// SG_Refused is v2.0.0's answer to a claim it will not take; with
	// RefusedTooOld it is the answer to this engine's SG_Chan.
	SG_Refused    byte = 9
	RefusedTooOld      = "too-old"
)

// LegacyClaim reports whether a control-channel claim was made the v2.0.0 way.
func LegacyClaim(signal byte) bool { return signal == SG_ChanV2 || signal == SG_ChanAuth }

// HandshakeError is a websocket upgrade the server answered with something
// other than the switch, and with what. A plain-engine server refuses a wrong
// token with 401; a v2.0.0 server answers this engine's claim with its decoy
// page instead, 200 or 404.
type HandshakeError struct {
	Status int
	Err    error
}

func (e *HandshakeError) Error() string { return fmt.Sprintf("%v (HTTP %d)", e.Err, e.Status) }
func (e *HandshakeError) Unwrap() error { return e.Err }

// LegacyServerAnswer reports whether a refused upgrade looks like v2.0.0's.
func (e *HandshakeError) LegacyServerAnswer() bool {
	return e.Status == http.StatusOK || e.Status == http.StatusNotFound
}
