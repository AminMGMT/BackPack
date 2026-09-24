package network

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"

	"github.com/quic-go/quic-go"
)

// Binding the reverse QUIC transport's credential to its TLS session.
//
// The QUIC client does not verify the server's certificate — the tunnel trusts
// its token, and the certificate is a throwaway the server generates at start.
// It then sent the token itself as the first thing on the control stream, and
// the server echoed it back as its answer. TLS hid that from anyone watching,
// but anything that terminated the TLS on the path — which an unverified
// certificate lets anybody do — read the token in both directions, and with it
// the whole tunnel.
//
// So the token is not sent. Each side exports keying material from the TLS
// session and the client proves it holds the token with an HMAC over that
// material; the server answers with an HMAC of its own, keyed the same way and
// labelled differently, which proves it holds the token too. A man in the
// middle holds two different TLS sessions, one with each end, so the proof one
// end computes is worthless on the other, and neither proof reveals the token.
//
// This is the WSS transport's binding (wssbind.go) carried over to QUIC, where
// the exporter is available on every connection.

// QUICBindingLabel is the TLS exporter label. Versioned, so a change to what is
// hashed is a change to the label and never a silent mismatch.
const QUICBindingLabel = "EXPORTER-backpack-quic-binding-v1"

const quicBindingLength = 32

// The two directions are labelled apart, so the server's answer can never be
// the client's proof sent back: a proof that is its own answer is an echo, and
// an echo is exactly what the old handshake was.
const (
	quicClientSide = "client"
	quicServerSide = "server"
)

func quicBinding(conn *quic.Conn, token, side string) (string, error) {
	cs := conn.ConnectionState().TLS
	ekm, err := cs.ExportKeyingMaterial(QUICBindingLabel, nil, quicBindingLength)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(side))
	mac.Write(ekm)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// QUICClientProof is what the client sends in place of the token, on the
// control stream and on every data stream of the connection.
func QUICClientProof(conn *quic.Conn, token string) (string, error) {
	return quicBinding(conn, token, quicClientSide)
}

// QUICServerProof is what the server answers a bound control claim with.
func QUICServerProof(conn *quic.Conn, token string) (string, error) {
	return quicBinding(conn, token, quicServerSide)
}

// QUICProofMatches reports, in constant time, whether got is the client proof
// for this connection and token.
func QUICProofMatches(conn *quic.Conn, token, got string) bool {
	want, err := QUICClientProof(conn, token)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
