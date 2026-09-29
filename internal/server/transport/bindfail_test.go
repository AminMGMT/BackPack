package transport

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/utils/acceptloop"
	"github.com/sirupsen/logrus"
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
	// The forwarded-port half lives in forward.go, shared by every stream
	// transport, and is run rather than read: see
	// TestATakenForwardedPortIsReportedAndTheOthersStillServe. The tunnel's own
	// listener is still each transport's own.
	for _, f := range transportSources {
		t.Run(f, func(t *testing.T) {
			src := withoutComments(readTransportSource(t, f))
			// The binding itself is bindTunnelPort's, run rather than read in
			// TestATakenTunnelPortIsRetriedUntilItIsFree; what is checked here is
			// that every transport goes through it.
			if !strings.Contains(src, "tunnelPort(g).serve()") && !strings.Contains(src, "bindTunnelPort(") {
				t.Error("the tunnel's own port is not bound through bindTunnelPort, so a port " +
					"held for a moment during a restart may leave the tunnel down until somebody notices")
			}
		})
	}
}

// A forwarded port that is taken is reported the way bindfail.go words it and
// skipped; the other ports of the same tunnel still serve.
func TestATakenForwardedPortIsReportedAndTheOthersStillServe(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	free := freeAddr(t)

	var logged syncBuffer
	log := quietLogger()
	log.SetOutput(&logged)
	f, stop := newTestForwarder(t, []string{taken.Addr().String(), free}, 0, log)
	defer stop()
	f.run()

	c := dialRetry(t, free)
	c.Close()
	eventually(t, 3*time.Second, func() bool {
		return strings.Contains(logged.String(), "forwarded port "+taken.Addr().String())
	}, "the taken port was not reported with its address")
}

// A mapping that cannot be read is one mapping.
//
// These parse errors were fatal too, which turned one typo in a config into the
// same three-second crash loop. Reported and skipped now, so the rest of the
// tunnel comes up and the operator has something to read.
func TestAnUnreadableMappingDoesNotEndTheProcess(t *testing.T) {
	var logged bytes.Buffer
	log := quietLogger()
	log.SetOutput(&logged)
	var bound []string
	eachForward([]string{"80=a=b", "notaport", "8080", "9000-9001=127.0.0.1:22"}, log,
		func(localAddr, target string) { bound = append(bound, localAddr+"→"+target) })

	for _, bad := range []string{"80=a=b", "notaport"} {
		if !strings.Contains(logged.String(), "ignoring the port mapping") || !strings.Contains(logged.String(), bad) {
			t.Errorf("the unreadable mapping %q was not reported: %s", bad, logged.String())
		}
	}
	want := []string{":8080→8080", ":9000→127.0.0.1:22", ":9001→127.0.0.1:22"}
	if strings.Join(bound, " ") != strings.Join(want, " ") {
		t.Errorf("bound %v, want %v — the readable mappings must still be served", bound, want)
	}
}

// syncBuffer is a log sink a test can read while the logger writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The tunnel port shared by tcp and tcpmux: a port that is taken is reported,
// waited for, and bound as soon as it is free — the tunnel does not exit and
// does not give up.
func TestATakenTunnelPortIsRetriedUntilItIsFree(t *testing.T) {
	holder, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := holder.Addr().String()

	var logs syncBuffer
	log := logrus.New()
	log.SetOutput(&logs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var set listenerSet
	admitted := make(chan struct{}, 1)
	port := tcpTunnelPort{ctx: ctx, addr: addr, keepAlive: time.Second, nodelay: true, preauth: &acceptloop.Gate{},
		listeners: &set, log: log, admit: func(c net.Conn) { c.Close(); admitted <- struct{}{} }}
	go port.serve()

	eventually(t, 3*time.Second, func() bool { return strings.Contains(logs.String(), "tunnel port") },
		"a taken tunnel port was not reported")
	holder.Close() // the previous instance lets go

	deadline := time.Now().Add(listenRetryFirst + 3*time.Second)
	for {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the tunnel port was never bound once it was free")
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case <-admitted:
	case <-time.After(2 * time.Second):
		t.Fatal("a connection on the bound port was never admitted")
	}
}
