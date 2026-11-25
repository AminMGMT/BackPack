package network

import "testing"

// The credential binding rests on one property: the proof is a function of both
// the TLS session (its exported keying material) and the token, so it cannot be
// carried from one session to another. These check exactly that, because it is
// what stops a man in the middle — who holds a different session with each side
// — from replaying the proof it received from the client on to the server.

func TestBindingProofIsDeterministic(t *testing.T) {
	const token = "a-real-looking-tunnel-token-0123456789"
	first := WSSBindingProof([]byte("keying-material-from-one-session"), token)
	again := WSSBindingProof([]byte("keying-material-from-one-session"), token)
	if first != again {
		t.Fatalf("the same session and token produced two different proofs: %s vs %s", first, again)
	}
}

// A different session must yield a different proof — otherwise a proof captured
// from the client's session would validate against the server's.
func TestBindingProofDiffersAcrossSessions(t *testing.T) {
	const token = "a-real-looking-tunnel-token-0123456789"
	clientSession := []byte("keying-material-client-side-aaaa")
	serverSession := []byte("keying-material-server-side-bbbb") // a MITM's other session

	if WSSBindingProof(clientSession, token) == WSSBindingProof(serverSession, token) {
		t.Fatal("two different sessions produced the same proof — a proof could be replayed across them")
	}
}

// A different token must yield a different proof, so knowing the session is not
// enough: the peer must hold the token.
func TestBindingProofDependsOnToken(t *testing.T) {
	ekm := []byte("keying-material-from-one-session")
	if WSSBindingProof(ekm, "token-one-0123456789abcdef") == WSSBindingProof(ekm, "token-two-0123456789abcdef") {
		t.Fatal("two different tokens produced the same proof")
	}
}

// The proof must not be, or reveal, the token — the whole point is that the
// token never travels.
func TestBindingProofIsNotTheToken(t *testing.T) {
	const token = "the-secret-token-should-not-appear"
	proof := WSSBindingProof([]byte("some-keying-material-here-000000"), token)
	if proof == token {
		t.Fatal("the proof is the token verbatim")
	}
	if len(proof) != 64 { // hex of 32-byte HMAC-SHA256
		t.Errorf("proof is %d chars, want 64 (hex sha256)", len(proof))
	}
}
