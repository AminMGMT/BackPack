package server

import "testing"

// A wss listener with no certificate gets one generated for it, and the name
// in it has to be something a client can actually be pointed at.
func TestCertHostNamesTheBindAddress(t *testing.T) {
	for in, want := range map[string]string{
		"1.2.3.4:443": "1.2.3.4",
		// Bound to everything, so it has no particular name of its own.
		"0.0.0.0:443": "localhost",
		"[::]:443":    "localhost",
		":443":        "localhost",
		"nonsense":    "localhost",
	} {
		if got := certHost(in); got != want {
			t.Fatalf("certHost(%q) = %q, want %q", in, got, want)
		}
	}
}
