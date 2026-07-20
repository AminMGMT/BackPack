package telegram

import (
	"errors"
	"testing"
)

// A tunnel restarted to add or migrate its relay port needs its peer to
// reconnect before the first request crosses; until then the send fails with a
// transport error that clears itself. SendTest retries on exactly those, so the
// set of errors it treats as transient is worth pinning down: too narrow and a
// freshly configured bot reports a failure that was about to succeed; too wide
// and a settled problem (the wrong proxy answering) hides behind a 20s wait.

func TestWarmingUpErrorsAreRetried(t *testing.T) {
	for _, msg := range []string{
		"Post \"https://api.telegram.org/...\": EOF",
		"dial tcp 127.0.0.1:41234: connect: connection refused",
		"read tcp 127.0.0.1:41234: read: connection reset by peer",
		"write tcp: write: broken pipe",
		"net/http: request canceled (i/o timeout)",
	} {
		if !isRelayWarmingUp(errors.New(msg)) {
			t.Errorf("a reconnecting tunnel throws %q, but SendTest would give up on it", msg)
		}
	}
}

func TestSettledFailuresAreNotRetried(t *testing.T) {
	for _, msg := range []string{
		"first record does not look like a TLS handshake",
		"x509: certificate signed by unknown authority",
		"no relay port configured for tunnel \"srv\"",
		"Telegram API error: 401 Unauthorized",
	} {
		if isRelayWarmingUp(errors.New(msg)) {
			t.Errorf("%q will not fix itself by waiting, but SendTest would retry for 20s", msg)
		}
	}
	if isRelayWarmingUp(nil) {
		t.Error("a nil error was treated as a transient failure")
	}
}
