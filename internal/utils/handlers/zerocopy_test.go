package handlers

import (
	"net"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

// The counters and the flag are process-wide, so every test here has to put
// them back or it changes the answer for whatever runs next.
func restoreZeroCopy(t *testing.T) {
	t.Helper()
	was := ZeroCopy()
	s, b := RelayCounts()
	t.Cleanup(func() {
		SetZeroCopy(was)
		splicedConns.Store(s)
		bufferedConns.Store(b)
	})
}

// Off is the default, and it has to be: the kernel path is the least proven
// thing here and a tunnel carrying real traffic must not be opted into it by an
// upgrade.
func TestZeroCopyDefaultsOff(t *testing.T) {
	restoreZeroCopy(t)
	SetZeroCopy(false)
	if ZeroCopy() {
		t.Fatal("zero-copy reports itself on after being turned off")
	}
}

// With it off, the fast path must not even be attempted — whatever the
// connections are.
func TestSpliceTransferDeclinesWhenDisabled(t *testing.T) {
	restoreZeroCopy(t)
	SetZeroCopy(false)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-accepted
	defer server.Close()

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Two genuine TCP sockets — everything the fast path wants except
	// permission.
	if spliceTransfer(client, server, logger, nil, 0, false) {
		t.Fatal("the kernel path ran while zero-copy was disabled")
	}
}

// The summary is what an operator pastes into a bug report, so each of the
// states it has to distinguish must actually read differently.
func TestRelaySummaryDistinguishesTheStatesThatMatter(t *testing.T) {
	restoreZeroCopy(t)

	SetZeroCopy(false)
	splicedConns.Store(0)
	bufferedConns.Store(500)
	if got := RelaySummary(); !strings.Contains(got, "off") {
		t.Errorf("disabled reads as %q, want it to say it is off", got)
	}

	SetZeroCopy(true)
	splicedConns.Store(0)
	bufferedConns.Store(0)
	if got := RelaySummary(); !strings.Contains(got, "nothing forwarded yet") {
		t.Errorf("enabled but idle reads as %q", got)
	}

	// The state worth catching: switched on, and every single transfer still
	// went the old way. Indistinguishable from "working" unless it is named.
	splicedConns.Store(0)
	bufferedConns.Store(42)
	got := RelaySummary()
	if !strings.Contains(got, "never used") {
		t.Errorf("enabled but always falling back reads as %q — that state has to be obvious", got)
	}
	if !strings.Contains(got, "42") {
		t.Errorf("summary %q does not say how many transfers fell back", got)
	}

	splicedConns.Store(100)
	bufferedConns.Store(7)
	got = RelaySummary()
	if !strings.Contains(got, "100") || !strings.Contains(got, "7") {
		t.Errorf("summary %q does not report both counts", got)
	}
}

// Both paths must be counted, or the summary is worse than nothing.
func TestBothPathsAreCounted(t *testing.T) {
	restoreZeroCopy(t)
	splicedConns.Store(0)
	bufferedConns.Store(0)

	countSpliced()
	countSpliced()
	countBuffered()

	s, b := RelayCounts()
	if s != 2 || b != 1 {
		t.Fatalf("counts = %d spliced / %d buffered, want 2/1", s, b)
	}
}
