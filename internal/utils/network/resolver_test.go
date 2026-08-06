package network

import "testing"

// A single destination must resolve exactly as it always has — a bare port
// means loopback, a full address is passed through. This is the path every
// existing tunnel takes, so it must not shift.
func TestResolveRemoteAddrSingle(t *testing.T) {
	for _, c := range []struct {
		in       string
		wantPort int
		wantAddr string
	}{
		{"443", 443, "127.0.0.1:443"},
		{"127.0.0.1:2096", 2096, "127.0.0.1:2096"},
		{"10.0.0.5:8443", 8443, "10.0.0.5:8443"},
		{"[2001:db8::10]:8443", 8443, "[2001:db8::10]:8443"},
	} {
		port, addr, err := ResolveRemoteAddr(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if port != c.wantPort || addr != c.wantAddr {
			t.Fatalf("%q = (%d, %q), want (%d, %q)", c.in, port, addr, c.wantPort, c.wantAddr)
		}
	}
}

// A pipe list names several backends: each is resolved to a full address, the
// list is rebuilt for the pool to split, and the reported port is the first
// backend's.
func TestResolveRemoteAddrBackendList(t *testing.T) {
	port, addr, err := ResolveRemoteAddr("8443|127.0.0.1:8444| 10.0.0.5:8445 ")
	if err != nil {
		t.Fatal(err)
	}
	if port != 8443 {
		t.Fatalf("port = %d, want the first backend's 8443", port)
	}
	want := "127.0.0.1:8443|127.0.0.1:8444|10.0.0.5:8445"
	if addr != want {
		t.Fatalf("addr = %q, want %q", addr, want)
	}
}

// A malformed entry anywhere in the list is an error, not a silently dropped
// backend — a typo must be visible rather than quietly halving the pool.
func TestResolveRemoteAddrBackendListRejectsGarbage(t *testing.T) {
	if _, _, err := ResolveRemoteAddr("8443|not-a-port"); err == nil {
		t.Fatal("a malformed backend should be an error")
	}
}
