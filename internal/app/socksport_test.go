package app

import "testing"

// The relay only works if both ends of a tunnel independently arrive at the
// same port. They never exchange it — the Iran side writes the mapping and the
// peer opens the listener — so the derivation has to be exactly reproducible.

func TestSocksPortIsStableForTheSameToken(t *testing.T) {
	const token = "a-real-looking-token-0123456789abcdef"

	first := SocksPortForToken(token)
	for i := 0; i < 100; i++ {
		if got := SocksPortForToken(token); got != first {
			t.Fatalf("call %d gave %d, first gave %d — the two ends would not agree", i, got, first)
		}
	}
}

func TestDifferentTokensGetDifferentPorts(t *testing.T) {
	seen := map[int]string{}
	collisions := 0

	for _, tok := range []string{
		"tunnel-one-0123456789abcdefghijklmno",
		"tunnel-two-0123456789abcdefghijklmno",
		"tunnel-three-0123456789abcdefghijkl",
		"short",
		"another-completely-different-token-x",
	} {
		p := SocksPortForToken(tok)
		if prev, dup := seen[p]; dup {
			collisions++
			t.Logf("%q and %q both map to %d", prev, tok, p)
		}
		seen[p] = tok
	}
	if collisions > 0 {
		t.Errorf("%d collisions across 5 tokens — the range is too narrow", collisions)
	}
}

// The port has to be usable: above the well-known range so it does not clash
// with a real service, and below the ephemeral range so the kernel will not
// hand it to an outgoing connection while the listener is down.
func TestPortLandsInASafeRange(t *testing.T) {
	for _, tok := range []string{"a", "b", "c", "dddddddddddddddddddddddd", "0", "~!@#$%^&*()"} {
		p := SocksPortForToken(tok)
		if p < 20000 || p >= 40000 {
			t.Errorf("SocksPortForToken(%q) = %d, outside the intended 20000–40000 window", tok, p)
		}
	}
}

// An unset token must not produce a random port: it falls back to the legacy
// fixed one, which is what old configs already point at.
func TestEmptyTokenFallsBackToTheLegacyPort(t *testing.T) {
	if got := SocksPortForToken(""); got != SocksInternalPort {
		t.Errorf("SocksPortForToken(\"\") = %d, want the legacy %d", got, SocksInternalPort)
	}
}

// It must not land on the legacy port for a real token, or an upgraded install
// would have two things trying to bind the same address.
func TestDerivedPortNeverCollidesWithLegacy(t *testing.T) {
	for _, tok := range []string{"x", "yy", "token-1", "token-2", "token-3"} {
		if got := SocksPortForToken(tok); got == SocksInternalPort {
			t.Errorf("token %q derived the legacy port %d", tok, got)
		}
	}
}
