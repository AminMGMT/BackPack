package webui

import (
	"testing"
)

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
