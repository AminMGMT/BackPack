package debugserver

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestPprofServerReleasesListenerOnCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeListener(ctx, listener) }()

	var response *http.Response
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response, err = http.Get("http://" + addr + "/debug/pprof/")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("pprof did not start: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("unexpected pprof response: status=%d headers=%v", response.StatusCode, response.Header)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pprof did not stop after cancellation")
	}

	rebound, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("pprof listener was not released: %v", err)
	}
	_ = rebound.Close()
}

func TestPprofServerLimits(t *testing.T) {
	server := newServer("127.0.0.1:0")
	if server.ReadHeaderTimeout != 10*time.Second || server.ReadTimeout != 15*time.Second ||
		server.IdleTimeout != 60*time.Second || server.MaxHeaderBytes != 16<<10 {
		t.Fatalf("unexpected pprof limits: %+v", server)
	}
}

func TestCancelledContextDoesNotBind(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Serve(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
}
