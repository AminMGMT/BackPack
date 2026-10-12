package tunnelspec

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/enginectl"
)

type startupEngine struct {
	name  string
	ready atomic.Int32 // -1: an older engine with no readiness field
}

func (e *startupEngine) Status() enginectl.Status {
	s := enginectl.Status{Name: e.name}
	if state := e.ready.Load(); state >= 0 {
		ready := state == 1
		s.HelperReady = &ready
	}
	return s
}

func (*startupEngine) RestartTransport() error { return nil }

func serveStartupEngine(t *testing.T, e *startupEngine) string {
	t.Helper()
	previous := enginectl.Dir
	enginectl.Dir = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- enginectl.Serve(ctx, "startup-fixture", e) }()
	t.Cleanup(func() {
		cancel()
		<-done
		enginectl.Dir = previous
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(enginectl.SocketPath("startup-fixture")); err == nil {
			return "startup-fixture"
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("engine socket did not start")
	return ""
}

func TestApplyReadinessRequiresTheHelperRatherThanALiveProcess(t *testing.T) {
	for _, tc := range []struct {
		name       string
		state      int32
		engineName string
		want       bool
	}{
		{"older-engine", -1, "startup-fixture", false},
		{"helper-starting", 0, "startup-fixture", false},
		{"helper-ready-without-peer", 1, "startup-fixture", true},
		{"wrong-engine", 1, "another-tunnel", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &startupEngine{name: tc.engineName}
			engine.ready.Store(tc.state)
			name := serveStartupEngine(t, engine)
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			if got := waitHelperReady(ctx, name); got != tc.want {
				t.Fatalf("readiness = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestApplyWaitsUntilTheHelperBecomesReady(t *testing.T) {
	engine := &startupEngine{name: "startup-fixture"}
	name := serveStartupEngine(t, engine)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	timer := time.AfterFunc(100*time.Millisecond, func() { engine.ready.Store(1) })
	defer timer.Stop()
	if !waitHelperReady(ctx, name) {
		t.Fatal("an initially pending helper was not accepted after readiness")
	}
}
