package debugserver

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// The bug in one test: turning profiling off, or reloading into a generation
// that also wants it, must find the port free. Nothing before this closed the
// listener, so the next generation raced a socket that was never going away.
func TestCancellationReleasesThePortForTheNextGeneration(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- ServeListener(ctx, listener) }()

	response := getWhenReady(t, "http://"+addr+"/debug/pprof/")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("pprof index returned %d", response.StatusCode)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("profiling responses went out without their headers: %v", response.Header)
	}

	cancel()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("serving ended with %v, want a clean stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the profiling server ignored cancellation")
	}

	// The point of the whole exercise: by the time ServeListener returns, the
	// next generation can have the port.
	rebound, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("the port was still held after the server returned: %v", err)
	}
	rebound.Close()
}

// Stands in for what any package in the build can do in an init function —
// registered once here, because the default mux panics on a repeat pattern and
// -count=N would otherwise hit that rather than the assertion below.
var _ = func() struct{} {
	http.DefaultServeMux.HandleFunc("/an-unrelated-package-did-this", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	return struct{}{}
}()

// Only the handlers this package names are on the port. Serving the default
// mux meant any package in the build could put something there.
func TestOnlyProfilingHandlersAreServed(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ServeListener(ctx, listener)

	getWhenReady(t, "http://"+addr+"/debug/pprof/")

	response, err := http.Get("http://" + addr + "/an-unrelated-package-did-this")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("a default-mux handler answered on the profiling port with %d", response.StatusCode)
	}
}

// A generation that is already over must not open a socket on its way out.
func TestAnEndedContextBindsNothing(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Serve(ctx, addr); err != nil {
		t.Fatalf("Serve on a cancelled context returned %v, want nil", err)
	}

	rebound, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("Serve bound the port anyway: %v", err)
	}
	rebound.Close()
}

// A long response — a CPU profile is 30 seconds by default — must not be cut
// off by a write deadline the server set behind the operator's back.
func TestNoWriteTimeoutCutsOffALongProfile(t *testing.T) {
	server := newServer("127.0.0.1:0")
	if server.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v; a CPU profile or a trace would be truncated", server.WriteTimeout)
	}
	if server.ReadHeaderTimeout == 0 || server.MaxHeaderBytes == 0 || server.IdleTimeout == 0 {
		t.Fatalf("the profiling server was built without its limits: %+v", server)
	}
}

// getWhenReady polls until the server is accepting, so the test does not race
// the listener coming up.
func getWhenReady(t *testing.T, url string) *http.Response {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := http.Get(url)
		if err == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			return response
		}
		if time.Now().After(deadline) {
			t.Fatalf("the profiling server never came up: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
