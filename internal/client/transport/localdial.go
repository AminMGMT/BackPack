package transport

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"

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

// Report logs a failed dial to the forwarded service, explaining what it means.
func (r *localDialReporter) Report(logger *logrus.Logger, addr string, err error) {
	r.mu.Lock()
	if t, seen := r.last[addr]; seen && time.Since(t) < localDialQuiet {
		r.mu.Unlock()
		return
	}
	r.last[addr] = time.Now()
	r.mu.Unlock()

	logger.Error(localDialMessage(addr, err))
}

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
