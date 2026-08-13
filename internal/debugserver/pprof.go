// Package debugserver hosts the opt-in profiling endpoint.
//
// It exists because of what `http.ListenAndServe(addr, nil)` does not do. That
// call serves the process-wide default mux and returns only when the server
// fails, so there was no server value to shut down and no way to say when it
// had stopped. Turning pprof off in the config did not close the listener that
// was already running: the reload built the next generation while the previous
// one still held 127.0.0.1:6060, and if that generation had profiling on too,
// it raced the port and lost.
//
// Serving the default mux is its own problem. Any package anywhere in the
// build that registers a handler in an init function — which is exactly how
// net/http/pprof itself works — becomes reachable on the profiling port
// without anyone deciding that it should be. The handlers here are named one
// by one instead, so what is on this port is what this file says is on it.
package debugserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

// shutdownGrace is how long an in-flight profile has to finish once the
// generation ends. A CPU profile is a 30-second request by default, so this is
// not a wait for the endpoint to go idle — it is a moment for a request that
// is nearly done, after which the listener is closed regardless.
const shutdownGrace = 5 * time.Second

// Serve runs the profiling endpoint on addr until ctx ends, then releases the
// port before returning. A context that is already over binds nothing.
func Serve(ctx context.Context, addr string) error {
	if ctx.Err() != nil {
		return nil
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	// The bind is not instant and the context can end during it, which would
	// otherwise leave a listener open with nobody left to close it.
	if ctx.Err() != nil {
		listener.Close()
		return nil
	}
	return ServeListener(ctx, listener)
}

// ServeListener is Serve on a listener the caller already opened. It owns the
// listener from here: it is closed by the time this returns, whichever way the
// server ended.
func ServeListener(ctx context.Context, listener net.Listener) error {
	server := newServer(listener.Addr().String())

	serving := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				// Graceful did not finish in time; take the port back anyway,
				// because the next generation is waiting for it.
				server.Close()
			}
		case <-serving:
			// Serve returned on its own — nothing to shut down.
		}
	}()

	err := server.Serve(listener)
	close(serving)
	// Not returning until the shutdown goroutine is done is what makes the
	// port free by the time the caller sees this return.
	<-closed

	if errors.Is(err, http.ErrServerClosed) || ctx.Err() != nil {
		return nil
	}
	return err
}

// newServer builds the profiling server with an explicit handler set and the
// limits a long-lived listener needs.
func newServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})

	return &http.Server{
		Addr:    addr,
		Handler: handler,
		// No WriteTimeout and no ReadTimeout that a profile could hit: a CPU
		// profile or a trace is a deliberately long response, and cutting it
		// off would break the one thing this port is for. The header timeout
		// and the header cap are what a slow client runs into instead.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}
