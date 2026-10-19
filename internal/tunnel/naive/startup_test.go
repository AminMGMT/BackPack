package naive

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func helperStartupFixture(t *testing.T) (string, string, []string, *logrus.Logger) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "BACKPACK_HELPER_OUTPUT_FIXTURE=1", "BACKPACK_HELPER_LISTEN="+addr)
	log := logrus.New()
	log.SetOutput(io.Discard)
	return bin, addr, env, log
}

func TestStartupRetriesARejectedPreflightWithoutStartingTheReverseEngine(t *testing.T) {
	bin, addr, env, log := helperStartupFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	states := make(chan bool, 16)
	ctx = WithReadiness(ctx, func(ready bool) { states <- ready })
	attempts := 0
	h, err := StartWithRetry(ctx, log, func() (*Helper, error) {
		attempts++
		childEnv := env
		if attempts == 1 {
			childEnv = append(append([]string(nil), env...), "BACKPACK_HELPER_CHECK_FAIL=1")
		}
		return startManaged(ctx, bin, "xray-server", []byte("{}"), addr, childEnv, log, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if attempts != 2 {
		t.Fatalf("preflight attempts = %d, want failed check followed by successful start", attempts)
	}
	for _, want := range []bool{false, false, true} {
		select {
		case got := <-states:
			if got != want {
				t.Fatal("readiness was not false for failed preflight and true only after the listener started")
			}
		case <-ctx.Done():
			t.Fatal("startup did not report readiness")
		}
	}
}

func TestStartupRetryStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, _, _, log := helperStartupFixture(t)
	h, err := StartWithRetry(ctx, log, func() (*Helper, error) {
		cancel()
		return nil, errors.New("preflight refused")
	})
	if h != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation did not end startup: %v", err)
	}
}

func TestHelperReadinessFallsOnExitAndReturnsAfterRestart(t *testing.T) {
	bin, addr, env, log := helperStartupFixture(t)
	pidfile := filepath.Join(t.TempDir(), "pid")
	env = append(env, "BACKPACK_HELPER_PID_FILE="+pidfile)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	states := make(chan bool, 16)
	ctx = WithReadiness(ctx, func(ready bool) { states <- ready })
	h, err := startManaged(ctx, bin, "xray-server", []byte("{}"), addr, env, log, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	await := func(want bool) {
		t.Helper()
		select {
		case got := <-states:
			if got != want {
				t.Fatalf("helper readiness = %v, want %v", got, want)
			}
		case <-ctx.Done():
			t.Fatal("helper readiness never changed")
		}
	}
	await(true)
	data, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	child, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Kill(); err != nil {
		t.Fatal(err)
	}
	await(false)
	await(true)
	h.Close()
	await(false)
	if _, err := os.Stat(h.dir); !os.IsNotExist(err) {
		t.Fatal("helper shutdown left its private configuration behind")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("helper shutdown still owns its listener: %v", err)
	}
	listener.Close()
}
