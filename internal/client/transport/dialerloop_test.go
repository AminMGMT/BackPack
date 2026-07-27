package transport

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every retry in the control-channel dialer has to pause.
//
// The loop dials, and on any failure starts again. Two of its error paths —
// the token write and the read deadline that follows it — returned to the top
// with no wait at all. They sit after a successful dial, so they are reached
// exactly when the far end accepts a connection and then drops it: a route
// being filtered mid-handshake, a stateful firewall closing a half-open
// connection, or the server's own tunnel service restarting.
//
// In that state the loop spun as fast as the kernel would open sockets. The
// backoff exists to stop precisely that; its own doc note says the fixed
// drumbeat it replaced "also looks like a scan to anything watching", and these
// paths were faster than the drumbeat.
//
// A behavioural test would need a server that accepts and resets on cue for
// each of six transports. The invariant is structural and reads directly off
// the source, so it is checked there.
func TestEveryDialerRetryBacksOff(t *testing.T) {
	// The loop body, from the backoff being created to the end of the function.
	dialer := regexp.MustCompile(`(?s)bo := newBackoff\(.*?\n}`)

	for _, name := range []string{"tcp.go", "tcpmux.go", "kcp.go", "udp.go", "ws.go", "wsmux.go"} {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		body := dialer.FindString(string(src))
		if body == "" {
			t.Errorf("%s: no backoff-driven dialer loop found", name)
			continue
		}

		lines := strings.Split(body, "\n")
		for i, line := range lines {
			if strings.TrimSpace(line) != "continue" {
				continue
			}
			// Look back over the few statements that make up this error path.
			var preceding string
			for j := i - 1; j >= 0 && j > i-8; j-- {
				preceding = lines[j] + "\n" + preceding
				if strings.Contains(lines[j], "if ") || strings.Contains(lines[j], "} else") {
					break
				}
			}
			if !strings.Contains(preceding, "bo.Wait(") {
				t.Errorf("%s: a retry near %q returns to the top without backing off — "+
					"if the far end accepts and drops, this loop spins",
					name, strings.TrimSpace(lines[max(0, i-3)]))
			}
		}
	}
}
