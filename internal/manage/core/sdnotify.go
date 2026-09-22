package core

import (
	"net"
	"os"
	"strconv"
	"time"
)

// Telling systemd this process is still working.
//
// Every unit this product installs says Type=simple, which means systemd knows
// one thing: whether the process exists. A process that is wedged — a goroutine
// deadlocked, a job that never returns, a loop stuck on a socket that will
// never answer — is a process systemd is perfectly happy with, and it is the
// exact failure an operator most needs to be told about, because from the
// outside it looks identical to a healthy service with nothing to do.
//
// sd_notify closes that gap without a dependency: it is a datagram to a unix
// socket whose path systemd puts in an environment variable. Thirty lines,
// no library, and nothing at all happens when the variable is absent — which
// is every case except running under systemd.
//
// # Why this is opt-in per unit
//
// A watchdog that fires kills and restarts the service. That is right for a
// wedged monitor and wrong for a tunnel engine mid-transfer, so it is applied
// where the failure it catches is worse than the restart it causes. See
// monitorservice.go.

// notifySocket is systemd's address, or "" when this is not running under it.
func notifySocket() string {
	s := os.Getenv("NOTIFY_SOCKET")
	if s == "" {
		return ""
	}
	// An abstract socket is written with a leading '@' and dialled with a NUL.
	if s[0] == '@' {
		return "\x00" + s[1:]
	}
	return s
}

// notify sends one line to systemd. Silent and best-effort: a service must not
// fail because it could not describe itself.
func notify(state string) {
	addr := notifySocket()
	if addr == "" {
		return
	}
	c, err := net.DialTimeout("unixgram", addr, time.Second)
	if err != nil {
		return
	}
	defer c.Close()
	_ = c.SetWriteDeadline(time.Now().Add(time.Second))
	_, _ = c.Write([]byte(state))
}

// NotifyReady tells systemd the service has finished starting.
func NotifyReady() { notify("READY=1") }

// NotifyStopping tells systemd a clean shutdown is under way, so the time spent
// closing listeners is not mistaken for a hang.
func NotifyStopping() { notify("STOPPING=1") }

// NotifyAlive is the heartbeat. systemd restarts the service if these stop
// arriving for longer than WatchdogSec.
func NotifyAlive() { notify("WATCHDOG=1") }

// WatchdogInterval is how often NotifyAlive should be called, derived from what
// systemd asked for.
//
// It returns half of WATCHDOG_USEC, which is the convention: the deadline is
// the interval, and sending at exactly the deadline means every scheduling
// hiccup is a restart. ok is false when systemd is not asking, which is the
// case for every unit that has not opted in and for every run outside systemd.
func WatchdogInterval() (every time.Duration, ok bool) {
	usec := os.Getenv("WATCHDOG_USEC")
	if usec == "" {
		return 0, false
	}
	// systemd sets WATCHDOG_PID when the notification must come from a
	// specific process; anything else should stay quiet rather than answer on
	// another process's behalf.
	if pid := os.Getenv("WATCHDOG_PID"); pid != "" && pid != strconv.Itoa(os.Getpid()) {
		return 0, false
	}
	n, err := strconv.ParseInt(usec, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return time.Duration(n) * time.Microsecond / 2, true
}
