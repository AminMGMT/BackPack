package enginectl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A stand-in engine, so the socket can be exercised without one.
type fakeEngine struct {
	status   Status
	restarts atomic.Int32
	refuse   error
}

func (f *fakeEngine) Status() Status { return f.status }
func (f *fakeEngine) RestartTransport() error {
	if f.refuse != nil {
		return f.refuse
	}
	f.restarts.Add(1)
	return nil
}

// serveTest starts a socket in a directory this test owns and returns the
// tunnel name to dial.
func serveTest(t *testing.T, h Handler) string {
	t.Helper()
	prev := Dir
	Dir = t.TempDir()
	t.Cleanup(func() { Dir = prev })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ready := make(chan error, 1)
	go func() { ready <- Serve(ctx, "fr-relay", h) }()

	// Serve blocks in Accept, so its readiness is the socket existing.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(SocketPath("fr-relay")); err == nil {
			return "fr-relay"
		}
		select {
		case err := <-ready:
			t.Fatalf("the socket never came up: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the socket never appeared")
	return ""
}

func TestTheEngineAnswersWhatItIsDoing(t *testing.T) {
	eng := &fakeEngine{status: Status{
		Name: "fr-relay", Role: "server", Transport: "wss",
		Connected: true, Peer: "203.0.113.9:51234",
		BytesIn: 1400, BytesOut: 2800, Generation: 3,
	}}
	name := serveTest(t, eng)

	var got Status
	if err := Ask(name, OpStatus, &got); err != nil {
		t.Fatalf("asking for status: %v", err)
	}
	if got != eng.status {
		t.Fatalf("status came back as %+v, want %+v", got, eng.status)
	}
}

// The whole point: a rung below `systemctl restart`, which keeps the process
// and everything the process is holding.
func TestTheTransportCanBeRestartedWithoutRestartingTheProcess(t *testing.T) {
	eng := &fakeEngine{}
	name := serveTest(t, eng)

	if err := Ask(name, OpRestartTransport, nil); err != nil {
		t.Fatalf("asking for a restart: %v", err)
	}
	if got := eng.restarts.Load(); got != 1 {
		t.Fatalf("the engine was asked to restart %d times, want 1", got)
	}
}

// A refusal reaches the caller as a refusal, in the engine's own words. A
// watchdog that read "the tunnel is between transports" as success would climb
// to the next rung for no reason.
func TestARefusedRestartIsReportedAsOne(t *testing.T) {
	eng := &fakeEngine{refuse: errors.New("the tunnel is between transports")}
	name := serveTest(t, eng)

	err := Ask(name, OpRestartTransport, nil)
	if err == nil {
		t.Fatal("a refused restart was reported as success")
	}
	if err.Error() != "the tunnel is between transports" {
		t.Fatalf("the engine's own words were lost: %v", err)
	}
	if eng.restarts.Load() != 0 {
		t.Fatal("the engine restarted despite refusing")
	}
}

// The closed list is the point: an op this engine does not know is refused by
// name, so a caller from a newer build learns that this engine is older rather
// than reading silence as success.
func TestAnUnknownOperationIsRefusedByName(t *testing.T) {
	name := serveTest(t, &fakeEngine{})
	err := Ask(name, "drop-the-pool", nil)
	if err == nil {
		t.Fatal("an operation this engine does not have was accepted")
	}
	if got := err.Error(); got != "this engine does not do drop-the-pool" {
		t.Fatalf("the refusal does not name what was asked: %q", got)
	}
}

// Ping is the question a process table cannot answer: a wedged engine is a
// running process.
func TestPingAnswers(t *testing.T) {
	name := serveTest(t, &fakeEngine{})
	if err := Ask(name, OpPing, nil); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

// No engine is a distinguishable answer, not a generic failure — a watchdog
// has to tell "not running" from "running and refusing".
func TestNoEngineIsItsOwnAnswer(t *testing.T) {
	prev := Dir
	Dir = t.TempDir()
	t.Cleanup(func() { Dir = prev })

	if err := Ask("nothing-here", OpPing, nil); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("a missing engine reported %v, want ErrNotRunning", err)
	}
}

// The socket is reachable only by something already root on this machine, which
// is the whole of the argument that it adds no attack surface.
func TestTheSocketIsNotReadableByAnybodyElse(t *testing.T) {
	name := serveTest(t, &fakeEngine{})

	fi, err := os.Stat(SocketPath(name))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("the socket is %o, want 600", perm)
	}
	dir, err := os.Stat(filepath.Dir(SocketPath(name)))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	// The directory is the test's TempDir here rather than /run/backpack, so
	// what is checked is that it is not world-anything — the mode Serve asks
	// for when it makes the directory itself is 0700.
	if dir.Mode().Perm()&0o007 != 0 {
		t.Errorf("the socket directory is %o and world-accessible", dir.Mode().Perm())
	}
}

// A socket left behind by a process that did not exit cleanly must not stop the
// next engine binding. Only one engine per tunnel exists — the systemd unit
// guarantees it — so removing a stale socket is safe.
func TestAStaleSocketDoesNotStopTheNextEngine(t *testing.T) {
	prev := Dir
	Dir = t.TempDir()
	t.Cleanup(func() { Dir = prev })

	if err := os.WriteFile(SocketPath("fr-relay"), []byte("stale"), 0o600); err != nil {
		t.Fatalf("planting a stale socket: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Serve(ctx, "fr-relay", &fakeEngine{}) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := Ask("fr-relay", OpPing, nil); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the engine never came up behind a stale socket")
}

// Several questions on one connection, because a caller that holds the socket
// open between them is doing the right thing.
func TestOneConnectionAnswersSeveralQuestions(t *testing.T) {
	eng := &fakeEngine{status: Status{Name: "fr-relay"}}
	name := serveTest(t, eng)

	c, err := Dial(name)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	for i := 0; i < 3; i++ {
		var got Status
		if err := c.Call(OpStatus, &got); err != nil {
			t.Fatalf("question %d: %v", i, err)
		}
		if got.Name != "fr-relay" {
			t.Fatalf("question %d came back as %+v", i, got)
		}
	}
}

// The socket goes when the engine does, so nothing is left for the next process
// to find and nothing answers for a tunnel that has stopped.
func TestTheSocketIsRemovedWhenTheEngineStops(t *testing.T) {
	prev := Dir
	Dir = t.TempDir()
	t.Cleanup(func() { Dir = prev })

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = Serve(ctx, "fr-relay", &fakeEngine{}) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := Ask("fr-relay", OpPing, nil); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(SocketPath("fr-relay")); os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the socket outlived the engine")
}
