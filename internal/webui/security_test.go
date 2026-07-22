package webui

import (
	"testing"
	"time"
)

// Five failures lock the address out; a success wipes the slate.
func TestLoginLimiter(t *testing.T) {
	l := &loginLimiter{fails: map[string]int{}, until: map[string]time.Time{}}
	ip := "203.0.113.9"

	for i := 0; i < loginMaxFails-1; i++ {
		l.fail(ip)
		if b, _ := l.blocked(ip); b {
			t.Fatalf("blocked after %d failures", i+1)
		}
	}
	l.fail(ip)
	if b, _ := l.blocked(ip); !b {
		t.Fatal("not blocked after reaching the limit")
	}

	l.reset(ip)
	if b, _ := l.blocked(ip); b {
		t.Fatal("still blocked after reset")
	}
}

// A code works exactly once; three wrong tries kill the pending login.
func TestTwoFAStore(t *testing.T) {
	st := &twoFAStore{pending: map[string]*pendingLogin{}}

	tok, code := st.start()
	if ok, _ := st.verify(tok, code); !ok {
		t.Fatal("correct code rejected")
	}
	if ok, dead := st.verify(tok, code); ok || !dead {
		t.Fatal("a code must not work twice")
	}

	tok, _ = st.start()
	for i := 0; i < twoFAMaxAttempts-1; i++ {
		if _, dead := st.verify(tok, "000000"); dead {
			t.Fatalf("killed after %d attempts", i+1)
		}
	}
	if _, dead := st.verify(tok, "000000"); !dead {
		t.Fatal("pending login should die after max attempts")
	}
}
