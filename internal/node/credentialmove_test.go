package node

import (
	"strings"
	"testing"
)

// Moving a server to a new address needs its password again.
//
// The stored password went with the server to whatever address it was given,
// and the new address's host key is accepted on first use — so anyone able to
// edit a server's address (a write-scoped panel token) could point it at a
// machine of their own and receive the root password in the next SSH login,
// without ever being able to read it. A new address with no password is
// refused now; the old one stays.
func TestMovingAServerNeedsItsPasswordAgain(t *testing.T) {
	dir := t.TempDir()
	defer pointStoreAt(t, dir)()
	if _, err := Add("de1", "203.0.113.7", 22, "root", "the-real-password"); err != nil {
		t.Fatal(err)
	}

	err := SetCredentials("de1", "198.51.100.66", 0, "", "")
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("moving the server without its password was allowed: %v", err)
	}
	if n, _ := Find("de1"); n.Host != "203.0.113.7" {
		t.Fatalf("the refused move still changed the address to %q", n.Host)
	}

	// With the password entered again it moves.
	if err := SetCredentials("de1", "198.51.100.66", 0, "", "entered-again"); err != nil {
		t.Fatalf("a move with the password was refused: %v", err)
	}
	// And a change that keeps the address needs nothing new.
	if err := SetCredentials("de1", "", 2222, "", ""); err != nil {
		t.Fatalf("a port change was refused: %v", err)
	}
}
