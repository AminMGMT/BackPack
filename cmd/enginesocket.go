package cmd

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/backpack/backpack/internal/enginectl"
	"github.com/backpack/backpack/internal/metrics"
)

// The engine's side of the control socket.
//
// It is deliberately thin. Everything it can answer is already known here —
// what the tunnel is, whether its control channel is up, what it has carried —
// and the one thing it can be asked to do is what a configuration change
// already does: end this generation so the loop starts the next one.
//
// That equivalence is the reason this is safe to add. A transport restart asked
// for over the socket takes exactly the path a reload takes, which is the path
// with years of production behind it, rather than a second way of stopping a
// transport that would have to be got right separately.

// engineControl is the handler behind the socket for one tunnel.
type engineControl struct {
	name      string
	role      string
	transport string

	mu sync.Mutex
	// restart ends the running generation. It is replaced on every generation,
	// because each one has its own context to cancel.
	restart func()

	generation atomic.Int64
}

// Status answers from the counters the engine already keeps.
//
// It reads the same values the metrics snapshot is built from rather than
// reading the snapshot: the file is written every thirty seconds, so answering
// from it would mean a live question with an answer up to half a minute old —
// and half a minute is long enough for the stall this exists to shorten.
func (e *engineControl) Status() enginectl.Status {
	in, out := metrics.Traffic()
	connected := false
	if c := metrics.SnapshotConnected(); c != nil {
		connected = *c
	}
	return enginectl.Status{
		Name:       e.name,
		Role:       e.role,
		Transport:  e.transport,
		Connected:  connected,
		Peer:       metrics.SnapshotPeer(),
		BytesIn:    in,
		BytesOut:   out,
		Generation: int(e.generation.Load()),
	}
}

// RestartTransport ends the running generation, which makes the engine loop
// start the next one.
//
// It returns as soon as the request is accepted. Waiting for the replacement to
// be carrying would mean holding the socket open across a dial on a path that
// is, by assumption, not working — which is the one situation where a caller
// most needs its question answered.
func (e *engineControl) RestartTransport() error {
	e.mu.Lock()
	stop := e.restart
	e.mu.Unlock()
	if stop == nil {
		return errNoGeneration
	}
	stop()
	return nil
}

// setGeneration records how to end the generation that is starting.
func (e *engineControl) setGeneration(stop func()) {
	e.mu.Lock()
	e.restart = stop
	e.mu.Unlock()
	e.generation.Add(1)
}

// errNoGeneration is what a restart asked for between generations reports. It
// is a real state and a short one: the loop is between cancelling one transport
// and starting the next.
var errNoGeneration = errNoGenerationType{}

type errNoGenerationType struct{}

func (errNoGenerationType) Error() string {
	return "the tunnel is between transports; ask again in a moment"
}

// serveEngineControl starts the socket for this tunnel, best effort.
//
// A socket that cannot be created is logged and nothing else. The control
// socket is a convenience for whatever is watching the tunnel; a tunnel that
// refused to carry traffic because /run was not writable would be trading the
// product for the diagnostic.
func serveEngineControl(ctx context.Context, e *engineControl) {
	go func() {
		if err := enginectl.Serve(ctx, e.name, e); err != nil {
			logger.Debugf("control socket unavailable: %v", err)
		}
	}()
}
