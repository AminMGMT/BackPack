package server

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/backpack/backpack/internal/utils/network"
)

// legacyWSClaim reports whether a /channel request is a Backpack v2.0.0
// client's: over ws its bearer is the v2 handshake's hello ("bp3."), over wss
// the proof bound to this TLS session.
func legacyWSClaim(r *http.Request, authHeader, token string) bool {
	bearer := strings.TrimPrefix(authHeader, "Bearer ")
	if strings.HasPrefix(bearer, network.PeerAuthWire+".") {
		return true
	}
	if r.TLS != nil {
		if want, err := network.WSSServerProof(r.TLS, token); err == nil {
			return subtle.ConstantTimeCompare([]byte(bearer), []byte(want)) == 1
		}
	}
	return false
}
