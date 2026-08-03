package network

import (
	"strings"
	"testing"
)

func TestNewPoolNonceIsUniqueAndFullLength(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		n, err := NewPoolNonce()
		if err != nil {
			t.Fatalf("NewPoolNonce: %v", err)
		}
		if len(n) != poolNonceHexLen {
			t.Fatalf("nonce is %d characters, want %d", len(n), poolNonceHexLen)
		}
		if seen[n] {
			t.Fatalf("NewPoolNonce repeated a value after %d draws", i)
		}
		seen[n] = true
	}
}

func TestControlAckRoundTrip(t *testing.T) {
	nonce, err := NewPoolNonce()
	if err != nil {
		t.Fatalf("NewPoolNonce: %v", err)
	}
	// Including tokens that would break a separator-based encoding, and both
	// mux versions plus "no version".
	for _, token := range []string{"t", "a-token", "", "token|with:odd\x00bytes", "0123456789abcdef"} {
		for _, version := range []int{0, 1, 2} {
			gotToken, gotNonce, gotVersion := DecodeControlAck(EncodeControlAck(token, nonce, version))
			if gotToken != token {
				t.Errorf("token %q round-tripped as %q", token, gotToken)
			}
			if gotNonce != nonce {
				t.Errorf("nonce round-tripped as %q, want %q", gotNonce, nonce)
			}
			if gotVersion != version {
				t.Errorf("mux version %d round-tripped as %d", version, gotVersion)
			}
		}
	}
}

// A malformed answer must decode to nothing, so the caller's token comparison
// fails and the server is rejected rather than half-trusted.
func TestDecodeControlAckRejectsShortInput(t *testing.T) {
	// The last one is nonce-length but carries no version byte at all.
	for _, ack := range []string{"", "short", "0123456789abcdef", strings.Repeat("a", poolNonceHexLen)} {
		token, nonce, version := DecodeControlAck(ack)
		if token != "" || nonce != "" || version != 0 {
			t.Errorf("DecodeControlAck(%q) = (%q, %q, %d), want empty", ack, token, nonce, version)
		}
	}
}

func TestPoolNonceVerify(t *testing.T) {
	var p PoolNonce

	// An unset nonce must refuse everything, including the empty string —
	// clearing it closes the door rather than opening it.
	if p.Verify("") {
		t.Error("an unset nonce accepted an empty value")
	}
	if p.Verify("anything") {
		t.Error("an unset nonce accepted a value")
	}

	nonce, err := NewPoolNonce()
	if err != nil {
		t.Fatalf("NewPoolNonce: %v", err)
	}
	p.Set(nonce)
	if !p.Verify(nonce) {
		t.Error("the current nonce was not accepted")
	}
	if p.Verify(nonce + "x") {
		t.Error("a longer value was accepted")
	}
	if p.Verify(nonce[:len(nonce)-1]) {
		t.Error("a truncated value was accepted")
	}

	p.Clear()
	if p.Verify(nonce) {
		t.Error("a cleared nonce still accepted the previous value")
	}
}
