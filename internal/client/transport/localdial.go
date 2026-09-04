package transport

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/sirupsen/logrus"
)

// Reporting a failed dial to the forwarded service.
//
// This is the most common failure a user will ever hit, and for a long time it
// read:
//
//	local dialer: dial tcp <nil>->127.0.0.1:4545: connect: connection refused
//
// which is accurate and nearly useless. It does not say which machine the
// problem is on, that the tunnel itself worked, or what to do. Someone seeing
// it concludes the tunnel is broken and uninstalls, which is the worst possible
// outcome for a failure that is usually a panel listening on the wrong address.
//
// So the message names the machine, says the tunnel did its part, gives the two
// real causes, and prints the command that distinguishes them. It is also
// rate-limited: a retrying client produces one of these per connection, and
// twenty identical lines a second buries whatever else the log had to say.

// localDialReporter rate-limits the "cannot reach the local service" message,
// one line per address per interval.
type localDialReporter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// localDialQuiet is how long to stay silent about an address already reported.
// Long enough that a client retrying once a second produces one line, short
// enough that the log still shows the problem is ongoing.
const localDialQuiet = 30 * time.Second

var localDial = &localDialReporter{last: map[string]time.Time{}}

// Report logs a failed dial to the forwarded service, explaining what it means,
// and records it where something other than a reader of the log can find it.
//
// The recording is not rate-limited with the logging. Suppressing repeats keeps
// the log readable; suppressing the count would make "refused four hundred
// connections" indistinguishable from "refused one", and that difference is the
// whole reading — a service that is down against a client that tried once while
// it restarted.
func (r *localDialReporter) Report(logger *logrus.Logger, addr string, err error) {
	metrics.ReportLocalDialFailure(addr, whyLocalDialFailed(err))

	r.mu.Lock()
	if t, seen := r.last[addr]; seen && time.Since(t) < localDialQuiet {
		r.mu.Unlock()
		return
	}
	r.last[addr] = time.Now()
	r.mu.Unlock()

	logger.Error(localDialMessage(addr, err))
}

// whyLocalDialFailed is the failure in one word, matching the three cases the
// message below distinguishes. The fix differs by case: a refusal is a service
// that is not listening, a timeout is usually a firewall on the same machine.
func whyLocalDialFailed(err error) string {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	case isTimeout(err):
		return "timeout"
	default:
		return "unreachable"
	}
}

// ReportLocalDialOK records that the last hop is working again. Called on every
// successful dial, so a run of failures ends the moment one connection lands.
func ReportLocalDialOK() { metrics.ReportLocalDialSuccess() }

// localDialMessage builds the explanation. Split out so it can be tested
// without a logger.
func localDialMessage(addr string, err error) string {
	var b strings.Builder

	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		fmt.Fprintf(&b, "nothing is listening on %s on THIS server.\n", addr)
		b.WriteString("  The tunnel delivered the connection correctly — the problem is the\n")
		b.WriteString("  service being forwarded to. Either it is not running, or it is bound\n")
		b.WriteString("  to a different address (many panels bind a public IP, not 127.0.0.1).\n")
		fmt.Fprintf(&b, "  Check with:  ss -tlnp | grep %s", portOnly(addr))

	case isTimeout(err):
		fmt.Fprintf(&b, "timed out connecting to %s on THIS server.\n", addr)
		b.WriteString("  The tunnel delivered the connection correctly. Something is listening\n")
		b.WriteString("  but not answering — a firewall rule on this machine, or a service\n")
		b.WriteString("  that is up but wedged.\n")
		fmt.Fprintf(&b, "  Check with:  ss -tlnp | grep %s", portOnly(addr))

	default:
		fmt.Fprintf(&b, "could not reach %s on THIS server: %v\n", addr, err)
		b.WriteString("  The tunnel delivered the connection correctly — this is the last hop,\n")
		b.WriteString("  from this machine to the service it forwards to.")
	}

	b.WriteString("\n  (repeats are suppressed for 30s)")
	return b.String()
}

// isTimeout reports whether err is a network timeout.
func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// portOnly returns the port from host:port, for use in the suggested command.
func portOnly(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 && i < len(addr)-1 {
		return addr[i+1:]
	}
	return addr
}
