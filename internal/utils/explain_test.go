package utils

import (
	"bytes"
	"strings"
	"testing"
)

// Each line an operator meets most often comes out in plain words with the
// fix, and the original stays at the end for support.
func TestCommonFailuresAreExplained(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"failed to start listener on 0.0.0.0:443: listen tcp 0.0.0.0:443: bind: address already in use", "Port 443 is already used"},
		{"failed to listen on :8080: listen tcp :8080: bind: address already in use", "Port 8080 is already used"},
		{"failed to listen on :80: listen tcp :80: bind: permission denied", "need root"},
		{"channel dialer: dial tcp 203.0.113.5:9000: connect: connection refused", "refused the connection"},
		{"tunnel server dialer: dial tcp 203.0.113.5:9000: i/o timeout", "does not answer"},
		{"local dialer: dial tcp 127.0.0.1:8080: connect: connection refused", "Nothing is listening at 127.0.0.1:8080"},
		{"control channel has been closed by the server", "The Iran server closed the tunnel"},
		{"control channel has been closed by the client", "The kharej server closed the tunnel"},
		{"failed to read from channel connection. EOF", "dropped"},
		{"timeout while waiting for control channel response", "took too long"},
		{"channel is full, cannot request a new connection", "channel_size"},
		{"unauthorized request from 198.51.100.7:5555, closing connection", "198.51.100.7:5555"},
		{"invalid port format: 44a3", "\"44a3\""},
		{"failed to create mux session: unsupported protocol version", "mux_version"},
		{"unexpected response from channel: 9", "same transport"},
	} {
		got := Explain(c.in)
		if !strings.Contains(got, c.want) {
			t.Errorf("%q\n  became %q\n  want it to say %q", c.in, got, c.want)
		}
		if !strings.Contains(got, explainMarker+c.in+"]") {
			t.Errorf("%q lost its original detail: %q", c.in, got)
		}
	}
}

// A line that carries the tunnel token is explained without repeating it.
func TestATokenNeverReachesTheLog(t *testing.T) {
	for _, in := range []string{
		"invalid token received. Expected: secret-one, Received: secret-two. Retrying...",
		"invalid security token received: secret-two",
	} {
		if got := Explain(in); strings.Contains(got, "secret") || got == in {
			t.Errorf("%q → %q", in, got)
		}
	}
}

// What no rule knows is left alone, and a line is never explained twice.
func TestUnknownAndExplainedLinesAreLeftAlone(t *testing.T) {
	in := "something nobody has seen before"
	if Explain(in) != in {
		t.Fatal("an unknown line was changed")
	}
	once := Explain("failed to read from channel connection. EOF")
	if Explain(once) != once {
		t.Fatal("an explained line was explained again")
	}
}

// The logger applies it to warnings and errors only.
func TestTheLoggerExplainsWarningsNotInfo(t *testing.T) {
	var buf bytes.Buffer
	log := NewLoggerWithFormat("info", "")
	log.SetOutput(&buf)
	log.Info("failed to read from channel connection. EOF")
	log.Warn("failed to read from channel connection. EOF")
	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || strings.Contains(lines[0], "dropped") || !strings.Contains(lines[1], "dropped") {
		t.Fatalf("want info untouched and warn explained:\n%s", out)
	}
}
