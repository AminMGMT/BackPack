package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"
)

// What happens when a port cannot be bound.
//
// Every listener in these transports used to answer a failed bind with
// logger.Fatalf, which is os.Exit(1). The unit these run under carries
// Restart=always and RestartSec=3, so an occupied port did not stop the tunnel
// — it put it in a three-second crash loop: bind, die, restart, bind, die.
//
// Measured on one forwarded port out of two: the control channel came up, the
// healthy port was never bound at all, the process was gone four seconds later,
// and the log's last line was
//
//	[FATAL] failed to listen on :62050: bind: address already in use
//
// From the far end that reads as a tunnel connecting and dropping every few
// seconds for no stated reason, which is exactly how it was reported.
//
// One busy port is a configuration mistake on one port. It is not a reason to
// take down a tunnel whose other ports are fine, and it is certainly not a
// reason to exit a process that a supervisor will immediately start again.
//
// The direct and layer-3 forwarders already had this right — they return the
// error and let the caller decide. This brings the reverse transports to the
// same place.

// bindFailure explains a listener that could not be opened.
//
// Written like the client's local-dial message and for the same reason: the
// bare error ("bind: address already in use") is accurate and nearly useless.
// It does not say which machine, which port, or what to do about it — and the
// operator reading it is usually looking at the wrong server. This says all
// three, and prints the command that names the process holding the port, which
// is the one thing the log cannot know and the operator can find in a second.
func bindFailure(what, addr string, err error) string {
	var b strings.Builder

	if isAddrInUse(err) {
		fmt.Fprintf(&b, "%s %s: something else on THIS server is already listening there.\n", what, addr)
		b.WriteString("  The tunnel did not take the port and cannot use it while that lasts.\n")
		b.WriteString("  It is usually the service being forwarded to, bound to the same port\n")
		b.WriteString("  the tunnel was told to expose, or an older copy of this tunnel that\n")
		b.WriteString("  has not exited yet.\n")
		fmt.Fprintf(&b, "  Find out which:  ss -tlnp | grep %s", portOf(addr))
		return b.String()
	}

	fmt.Fprintf(&b, "%s %s could not be opened: %v", what, addr, err)
	return b.String()
}

// isAddrInUse reports whether a listen failed because the port was taken. The
// error arrives wrapped in net.OpError, so it is unwrapped rather than matched
// on its text — the message is the kernel's and is not ours to depend on.
func isAddrInUse(err error) bool { return errors.Is(err, syscall.EADDRINUSE) }

// portOf is the port from a listen address, for the command printed above.
// ":62050" and "1.2.3.4:62050" both give "62050"; anything else is handed back
// whole, which still makes a usable grep.
func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 && i+1 < len(addr) {
		return addr[i+1:]
	}
	return addr
}

// Retrying a listener that could not be bound.
//
// The port a tunnel itself listens on is not optional: without it there is no
// tunnel, so a failed bind there cannot simply be skipped the way a forwarded
// port can. Exiting is still the wrong answer — the supervisor restarts the
// process in three seconds and it fails again, forever, while the log fills
// with the same line.
//
// Waiting and trying again is what the situation actually calls for. The two
// reasons a tunnel port is briefly taken — a previous instance still shutting
// down, or the port in TIME_WAIT — both clear on their own in seconds. The
// backoff is measured in seconds for that reason, where acceptBackoff next door
// is measured in milliseconds: that one paces a loop that is spinning, this one
// waits for another process to let go.
type listenBackoff struct {
	delay time.Duration
}

const (
	listenRetryFirst = 2 * time.Second
	listenRetryMax   = 30 * time.Second
)

// wait pauses before the next attempt, returning false when the run ended while
// waiting so the caller can stop rather than bind a port for a tunnel that is
// going away.
func (b *listenBackoff) wait(ctx context.Context) bool {
	if b.delay == 0 {
		b.delay = listenRetryFirst
	} else if b.delay < listenRetryMax {
		b.delay *= 2
		if b.delay > listenRetryMax {
			b.delay = listenRetryMax
		}
	}
	t := time.NewTimer(b.delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
