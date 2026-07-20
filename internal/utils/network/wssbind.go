package network

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"

	utls "github.com/refraction-networking/utls"
)

var errNoTLSState = errors.New("no TLS connection state")

// Binding the WSS credential to the TLS session.
//
// A WSS tunnel verifies nothing about the server's certificate — it dials with
// InsecureSkipVerify, because the tunnel trusts its token, not a certificate
// authority, and often the certificate is self-signed anyway. That is fine for
// confidentiality against a passive observer, but it leaves a gap against an
// active one: on a path the operator does not control, something can present its
// own certificate, terminate the TLS itself, and read whatever the client sends
// next — including the bearer token, which is all an impostor needs to become
// the client.
//
// So the token is never sent. Instead each side derives a value from the TLS
// session that only the two genuine endpoints share — RFC 5705 exported keying
// material — and the client proves it holds the token by sending
// HMAC(token, keying material). A man in the middle terminating TLS has two
// different sessions, one with each side, so its keying material does not match
// the server's; it cannot turn the proof it received from the client into the
// proof the server expects, and it never learns the token to forge one. The
// binding costs nothing on the wire — it is the same Authorization header as
// before — and works the same whether the certificate is self-signed or from
// Let's Encrypt, because it depends on the session, not the certificate.

// WSSBindingLabel is the RFC 5705 exporter label for the credential binding.
const WSSBindingLabel = "EXPORTER-backpack-wss-binding-v1"

// wssBindingLength is how many bytes of keying material to export.
const wssBindingLength = 32

// WSSBindingProof returns the value the client sends and the server checks:
// HMAC-SHA256 of the exported keying material, keyed by the tunnel token.
func WSSBindingProof(ekm []byte, token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write(ekm)
	return hex.EncodeToString(mac.Sum(nil))
}

// wssClientBinding exports the keying material from a completed client handshake
// and returns the proof to put in the Authorization header. It fails closed: if
// the material cannot be exported (a TLS version or handshake that does not
// support RFC 5705 exporters), no proof is produced and the caller must abort
// rather than fall back to sending the token in the clear.
func wssClientBinding(uconn *utls.UConn, token string) (string, error) {
	cs := uconn.ConnectionState()
	ekm, err := cs.ExportKeyingMaterial(WSSBindingLabel, nil, wssBindingLength)
	if err != nil {
		return "", err
	}
	return WSSBindingProof(ekm, token), nil
}

// WSSServerProof computes the proof the server expects for a connection, from
// the same keying material the client exported. A man in the middle that
// terminated the client's TLS has a different session here, so its material —
// and therefore this proof — does not match what the client sent. It fails
// closed for the same reason wssClientBinding does.
func WSSServerProof(cs *tls.ConnectionState, token string) (string, error) {
	if cs == nil {
		return "", errNoTLSState
	}
	ekm, err := cs.ExportKeyingMaterial(WSSBindingLabel, nil, wssBindingLength)
	if err != nil {
		return "", err
	}
	return WSSBindingProof(ekm, token), nil
}
