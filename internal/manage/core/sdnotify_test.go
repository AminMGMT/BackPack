package core

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Telling systemd this process is still working.
//
// The whole point is the difference between a process that exists and one that
// is doing its job. What has to be right is the quiet case: outside systemd
// there is no socket and no variable, and none of this may do anything at all
// — including fail, block, or log.

func TestNothingHappensOutsideSystemd(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	t.Setenv("WATCHDOG_USEC", "")

	// None of these may block or panic with nowhere to send to.
	NotifyReady()
	NotifyAlive()
	NotifyStopping()

	if _, ok := WatchdogInterval(); ok {
		t.Error("a watchdog interval was reported with no systemd asking for one")
	}
}

// A socket that is not there must not hold the caller up. These are called
// from a service's startup path and from a ticker.
func TestAMissingSocketDoesNotBlock(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "nothing-here"))

	done := make(chan struct{})
	go func() { defer close(done); NotifyReady() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("NotifyReady blocked on a socket that does not exist")
	}
}

func TestTheNotificationReachesTheSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notify")

	addr, err := net.ResolveUnixAddr("unixgram", path)
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	conn, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		t.Skipf("no unixgram socket available here: %v", err)
	}
	defer conn.Close()

	t.Setenv("NOTIFY_SOCKET", path)
	NotifyReady()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("nothing arrived: %v", err)
	}
	if got := string(buf[:n]); got != "READY=1" {
		t.Fatalf("systemd was sent %q", got)
	}
}

// The heartbeat goes out at half the deadline. Sending at exactly the deadline
// makes every scheduling hiccup a restart.
func TestTheHeartbeatIntervalIsHalfTheDeadline(t *testing.T) {
	t.Setenv("WATCHDOG_USEC", strconv.Itoa(120*1_000_000))
	t.Setenv("WATCHDOG_PID", "")

	every, ok := WatchdogInterval()
	if !ok {
		t.Fatal("no interval reported")
	}
	if every != 60*time.Second {
		t.Fatalf("interval = %v, want half of the 120s deadline", every)
	}
}

// A watchdog addressed to another process is not this one's to answer.
func TestAWatchdogForAnotherProcessIsIgnored(t *testing.T) {
	t.Setenv("WATCHDOG_USEC", strconv.Itoa(30*1_000_000))
	t.Setenv("WATCHDOG_PID", strconv.Itoa(os.Getpid()+1))

	if _, ok := WatchdogInterval(); ok {
		t.Error("answered a watchdog addressed to a different process")
	}
}

func TestAnUnreadableDeadlineIsIgnored(t *testing.T) {
	for _, v := range []string{"", "soon", "0", "-5"} {
		t.Setenv("WATCHDOG_USEC", v)
		t.Setenv("WATCHDOG_PID", "")
		if _, ok := WatchdogInterval(); ok {
			t.Errorf("WATCHDOG_USEC=%q was accepted", v)
		}
	}
}

// systemd writes an abstract socket with a leading '@', which has to be dialled
// with a NUL byte instead. Getting this wrong means the notifications go
// nowhere and the service is killed by its own watchdog.
func TestAnAbstractSocketAddressIsTranslated(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "@/org/freedesktop/systemd1/notify")
	got := notifySocket()
	if len(got) == 0 || got[0] != 0 {
		t.Fatalf("abstract address became %q; it has to start with a NUL or the "+
			"notifications go nowhere and systemd kills the service", got)
	}
	if got[1:] != "/org/freedesktop/systemd1/notify" {
		t.Fatalf("the rest of the address was changed: %q", got[1:])
	}
}
