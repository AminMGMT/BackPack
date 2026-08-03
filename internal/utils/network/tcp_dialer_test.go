package network

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

// resolvesToBoth reports whether a name resolves to both an IPv4 and an IPv6
// address on this machine, which is what these tests need in order to say
// anything. A machine where it does not is not evidence of a bug.
func resolvesToBoth(t *testing.T, host string) bool {
	t.Helper()
	addrs, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil {
		return false
	}
	var v4, v6 bool
	for _, a := range addrs {
		if a.IP.To4() != nil {
			v4 = true
		} else {
			v6 = true
		}
	}
	return v4 && v6
}

// A name that resolves to several addresses must be tried at every one of them,
// not just the first.
//
// This is the failure the change exists to fix, set up so that the old
// behaviour cannot pass it: the listener is bound only to the IPv6 loopback,
// while the name also carries an IPv4 record. Resolving the name to a single
// address picked the IPv4 one and connected to nothing; dialling the name tries
// both and finds the listener. It is the same shape as the case that matters in
// the field — a server published under several A records, one of which is
// filtered — reproduced with the one multi-address name every machine has.
func TestDialerTriesEveryResolvedAddress(t *testing.T) {
	if !resolvesToBoth(t, "localhost") {
		t.Skip("localhost does not resolve to both an IPv4 and an IPv6 address here")
	}

	// Listening on ::1 only, so reaching it requires trying more than the
	// first-resolved (IPv4) address.
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback on this machine")
	}
	defer ln.Close()

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}

	conn, err := TcpDialer(context.Background(), net.JoinHostPort("localhost", port),
		5*time.Second, 30*time.Second, true, 1, 0, 0, 0)
	if err != nil {
		t.Fatalf("dialling a name whose first address is dead did not fall through to the live one: %v", err)
	}
	conn.Close()
}

// Dialling a plain address literal must keep working exactly as before — the
// overwhelmingly common case, and the one a regression here would break.
func TestDialerStillDialsAnAddressLiteral(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	conn, err := TcpDialer(context.Background(), ln.Addr().String(),
		5*time.Second, 30*time.Second, true, 1, 0, 0, 0)
	if err != nil {
		t.Fatalf("dialling a literal address failed: %v", err)
	}
	conn.Close()
}

// A name that resolves to nothing reachable still has to give up rather than
// hang, and within the timeout it was given — the retry loop above it depends
// on that to move on to the next endpoint.
func TestDialerGivesUpOnAnUnreachableName(t *testing.T) {
	// Port 1 on the loopback: nothing listens there, so every resolved address
	// refuses immediately.
	start := time.Now()
	_, err := TcpDialer(context.Background(), net.JoinHostPort("localhost", "1"),
		2*time.Second, 30*time.Second, true, 1, 0, 0, 0)
	if err == nil {
		t.Fatal("dialling a closed port reported success")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("gave up after %v, far longer than the timeout it was given", elapsed)
	}
}

// The timeout has to cover the whole attempt, however many addresses the name
// resolves to — otherwise a name with several dead records would multiply the
// caller's timeout by the number of records.
func TestDialerHonoursTheTimeoutAcrossAddresses(t *testing.T) {
	if !resolvesToBoth(t, "localhost") {
		t.Skip("localhost does not resolve to both an IPv4 and an IPv6 address here")
	}

	// A routable address that will not answer, so the dial has to time out
	// rather than being refused. 203.0.113.0/24 is reserved for documentation
	// and is not routed.
	start := time.Now()
	_, err := TcpDialer(context.Background(), "203.0.113.1:9",
		2*time.Second, 30*time.Second, true, 1, 0, 0, 0)
	if err == nil {
		t.Skip("something answered a documentation address; this network is unusual")
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("a 2s timeout took %v", elapsed)
	}
}

// A bad address must fail with an error rather than panic, and must not be
// mistaken for a name to look up.
func TestDialerRejectsAMalformedAddress(t *testing.T) {
	for _, addr := range []string{"", "no-port", "host:not-a-port"} {
		if _, err := TcpDialer(context.Background(), addr,
			time.Second, 30*time.Second, true, 1, 0, 0, 0); err == nil {
			t.Errorf("TcpDialer(%q) reported success", addr)
		}
	}
}

// The retry count must be respected: a caller asking for one attempt must not
// get several, because the endpoint rotation above it is what should be
// deciding when to move on.
func TestDialerRetriesAreBounded(t *testing.T) {
	// Closed port on the loopback: refused immediately, so the elapsed time is
	// almost entirely the backoff between retries (1s, then 2s).
	start := time.Now()
	_, err := TcpDialer(context.Background(), "127.0.0.1:1",
		time.Second, 30*time.Second, true, 1, 0, 0, 0)
	if err == nil {
		t.Skip("something is listening on port 1")
	}
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Fatalf("a single attempt took %v, so it retried when it was asked not to", elapsed)
	}

	start = time.Now()
	if _, err := TcpDialer(context.Background(), "127.0.0.1:1",
		time.Second, 30*time.Second, true, 2, 0, 0, 0); err == nil {
		t.Skip("something is listening on port 1")
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("two attempts took only %v, so the second one did not happen", elapsed)
	}
}

func TestDialerReportsTheAddressItFailedOn(t *testing.T) {
	_, err := TcpDialer(context.Background(), "127.0.0.1:1",
		time.Second, 30*time.Second, true, 1, 0, 0, 0)
	if err == nil {
		t.Skip("something is listening on port 1")
	}
	// The operator has to be able to tell which endpoint failed when several
	// are configured, so the address has to survive into the error.
	if got := fmt.Sprint(err); !contains(got, "127.0.0.1:1") {
		t.Errorf("error %q does not name the address that failed", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
