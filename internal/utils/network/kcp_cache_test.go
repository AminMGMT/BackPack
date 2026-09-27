package network

import (
	"testing"
	"time"
)

// The KCP key is derived with a deliberately slow PBKDF2. It depends on the
// token alone, so only the first derivation for a token may pay for it — the
// client derives one per session it dials.
func TestTheKCPKeyIsDerivedOncePerToken(t *testing.T) {
	const token = "a-token-only-this-test-uses"
	start := time.Now()
	if _, err := kcpCrypt(token); err != nil {
		t.Fatal(err)
	}
	first := time.Since(start)

	start = time.Now()
	for i := 0; i < 20; i++ {
		if _, err := kcpCrypt(token); err != nil {
			t.Fatal(err)
		}
	}
	if again := time.Since(start) / 20; again*5 > first {
		t.Fatalf("a repeat derivation took %s against %s for the first — the key is not being reused", again, first)
	}
}
