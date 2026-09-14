package transport

import (
	"strings"
	"testing"
)

// transportSources is every transport whose listeners this guards.
var transportSources = []string{
	"tcp.go", "tcpmux.go", "ws.go", "wsmux.go", "kcp.go", "quic.go", "udp.go",
}

// A port that cannot be bound must not end the process.
//
// Every listener in these transports used to answer a failed bind with
// logger.Fatalf, which is os.Exit(1). The unit carries Restart=always and
// RestartSec=3, so an occupied port did not stop the tunnel — it put it in a
// three-second crash loop. Measured with one forwarded port of two taken: the
// control channel came up, the healthy port was never bound, and the process
// was gone four seconds later.
//
// Fatalf is the whole hazard, so this guards the word itself rather than any
// one call: reintroducing it anywhere in a transport brings the crash loop
// back, whatever the message says.
func TestNoTransportEndsTheProcessOnAFailedListen(t *testing.T) {
	for _, f := range transportSources {
		t.Run(f, func(t *testing.T) {
			src := withoutComments(readTransportSource(t, f))
			if strings.Contains(src, "Fatalf") || strings.Contains(src, "Fatal(") {
				t.Error("this transport still ends the process on a failure; under a unit " +
					"that restarts every three seconds that is a crash loop, not a stop")
			}
		})
	}
}

// The two halves of the fix, which are not interchangeable.
//
// A forwarded port that cannot be bound is skipped: the tunnel and its other
// ports are unaffected, and waiting would not help because the port belongs to
// something else. The tunnel's own port cannot be skipped — without it there is
// no tunnel — so that one is retried, because the two things that hold it (a
// previous instance shutting down, and TIME_WAIT) both clear on their own.
func TestAForwardedPortIsSkippedAndTheTunnelPortIsRetried(t *testing.T) {
	for _, f := range transportSources {
		t.Run(f, func(t *testing.T) {
			src := withoutComments(readTransportSource(t, f))

			if !strings.Contains(src, `bindFailure("forwarded port"`) {
				t.Error("no forwarded-port listener reports a bind failure the way bindfail.go " +
					"does, so the operator is not told which port or how to find the holder")
			}
			if !strings.Contains(src, `bindFailure("tunnel port"`) {
				t.Error("the tunnel's own listener does not report a bind failure")
			}
			if !strings.Contains(src, "listenBackoff") {
				t.Error("the tunnel's own port is not retried, so a port held for a moment " +
					"during a restart leaves the tunnel down until somebody notices")
			}
		})
	}
}

// A mapping that cannot be read is one mapping.
//
// These parse errors were fatal too, which turned one typo in a config into the
// same three-second crash loop. Reported and skipped now, so the rest of the
// tunnel comes up and the operator has something to read.
func TestAnUnreadableMappingDoesNotEndTheProcess(t *testing.T) {
	for _, f := range transportSources {
		t.Run(f, func(t *testing.T) {
			src := withoutComments(readTransportSource(t, f))
			if !strings.Contains(src, "ignoring the port mapping") {
				t.Error("an unreadable port mapping is not reported and skipped")
			}
		})
	}
}
