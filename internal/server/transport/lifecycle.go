package transport

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils/acceptloop"
	"github.com/backpack/backpack/internal/web"
	"github.com/sirupsen/logrus"
)

// lifecycle is the part of every reverse server transport that owns its runs.
//
// A transport runs in generations. A generation is one control channel and
// everything that serves it — the tunnel listener, the forwarded ports, the
// pairing workers — sharing one context. When the control channel is lost the
// transport ends the generation and starts the next one in the same process.
// That sequence was written out seven times, once per transport, and every
// copy had to get the same things right: one restart at a time, waiting for the
// old generation's ports instead of sleeping, not rebuilding anything once the
// whole tunnel is shutting down, and clearing the peer on every way out. It is
// here once now; a transport supplies only how to close its control channel and
// how to build and start its next generation.
//
// What outlives a generation is exactly what is on this struct and the
// transport's configuration-derived fields (limits, counters); everything a
// generation owns is built fresh for it and handed to its start function.
type lifecycle struct {
	// parentctx is the tunnel's context. Every generation's context derives
	// from it, and a restart finding it done builds nothing.
	parentctx context.Context
	logger    *logrus.Logger
	// usage is what each generation's usage monitor is built from.
	usage usageSpec

	// run is the current generation's context. Replaced by a restart while the
	// previous generation's goroutines may still be reading it, so it lives
	// behind a lock.
	run runState

	// listeners counts the listeners this transport holds right now, so a
	// restart — and Wait — can know the ports are free rather than guess.
	// See listeners.go.
	listeners listenerSet

	// status is what the panel shows. Behind a lock because the generation
	// being replaced and the one replacing it both write it.
	status tunnelStatus

	restartMutex sync.Mutex
	counterMutex sync.Mutex

	// preauth bounds the connections held before they have proved the token.
	// It outlives the generations: a flood does not start over at a restart.
	preauth acceptloop.Gate

	// rivals notices two clients with one token taking the seat from each
	// other. See seated.
	rivals rivalry
}

// countGeneration serializes late session/stream completion against a restart.
// Once the generation ends, its counters will be reset by the next run and
// must no longer be changed by its retiring workers.
func (l *lifecycle) countGeneration(ctx context.Context, counter *int32, delta int32) bool {
	l.counterMutex.Lock()
	defer l.counterMutex.Unlock()
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	atomic.AddInt32(counter, delta)
	return true
}

// seated records the client that has just taken the seat: the engine reports
// it as the peer (see metrics.Snapshot.Connected), and two clients taking
// turns are named in the log.
func (l *lifecycle) seated(addr string) {
	metrics.ReportPeer(addr)
	if hosts, ok := l.rivals.seat(addr, time.Now()); ok {
		l.logger.Warnf("two clients with the same token are taking this tunnel from each other (%s): "+
			"each one's claim ends the other's. That is two kharej servers set up as one tunnel — "+
			"give each its own tunnel, with its own port and token.", strings.Join(hosts, " and "))
	}
}

// usageSpec is what a generation's usage monitor is built from.
type usageSpec struct {
	webPort    int
	snifferLog string
	sniffer    bool
}

// usageMonitor builds the usage monitor for a generation running on ctx.
func (l *lifecycle) usageMonitor(ctx context.Context) *web.Usage {
	return web.NewDataStore(fmt.Sprintf(":%v", l.usage.webPort), ctx, l.usage.snifferLog, l.usage.sniffer, l.status.get, l.logger)
}

// firstGeneration installs the first run's context the same way every later
// one is installed, so there is only one path that ever writes it.
func (l *lifecycle) firstGeneration() {
	ctx, cancel := context.WithCancel(l.parentctx)
	l.run.set(ctx, cancel)
}

// Running reports whether a control channel is up.
func (l *lifecycle) Running() bool { return connected(l.status.get()) }

// Wait blocks until the transport holds no listener, or ctx ends.
func (l *lifecycle) Wait(ctx context.Context) { l.listeners.wait(ctx) }

// restart ends the running generation and, unless the whole tunnel is shutting
// down, starts the next: closeControl closes the generation's control channel,
// and next resets whatever per-generation state the transport keeps and starts
// the new generation on ctx.
func (l *lifecycle) restart(closeControl func(), next func(ctx context.Context)) {
	if !l.restartMutex.TryLock() {
		l.logger.Warn("server restart already in progress, skipping restart attempt")
		return
	}
	defer l.restartMutex.Unlock()

	l.logger.Info("restarting server...")

	// The teardown produces a burst of timeout errors from goroutines that are
	// being told to stop; they say nothing an operator can act on.
	level := l.logger.GetLevel()
	l.logger.SetLevel(logrus.FatalLevel)

	l.run.stop()
	closeControl()

	// Wait for the listeners rather than guessing at how long they take.
	//
	// This was a flat two-second sleep: the generation being replaced still
	// holds the ports, and binding them again before it lets go fails. A sleep
	// is a guess — usually long enough, never a guarantee, and silently wrong
	// on a loaded machine, which is exactly when a restart is most likely.
	// listenerSet answers the question instead, and in the ordinary case
	// returns at once.
	l.listeners.wait(l.parentctx)

	// The whole tunnel may have been shut down while this restart was waiting —
	// on a reload, or on the process going down. Rebuilding from a parent that
	// is already finished would bind the listeners only to close them, and on
	// a reload that means fighting the run replacing this one for its ports.
	if l.parentctx.Err() != nil {
		l.logger.SetLevel(level)
		// Abandoning is not a reason to keep claiming a peer. With a transport
		// fallback chain the process keeps running after this, and a snapshot
		// with a fresh timestamp and a connected peer for a tunnel that has
		// nothing connected is what the watchdog would read as healthy.
		l.status.set("")
		metrics.ClearPeer()
		l.logger.Debug("restart abandoned: the tunnel is shutting down")
		return
	}

	ctx, cancel := context.WithCancel(l.parentctx)
	l.run.set(ctx, cancel)

	l.status.set("")
	metrics.ClearPeer()
	l.logger.SetLevel(level)

	// The next generation's state is built by next and handed straight to its
	// start function. It used to be written onto the transport for start to
	// read back — a value published by one goroutine and read by another with
	// nothing ordering them; passing it removes the shared field instead of
	// locking it.
	next(ctx)
}

// pairingWorkers is how many goroutines pair queued users with tunnel
// connections: one per CPU, up to four.
func pairingWorkers() int { return min(runtime.NumCPU(), 4) }

// serveGeneration starts what a generation keeps for as long as it lives — the
// forwarded ports and the pairing workers — whichever client is seated. See
// clientSeat.
func (l *lifecycle) serveGeneration(forwarder portForwarder, pair func()) {
	go forwarder.run()
	n := pairingWorkers()
	l.logger.Infof("starting %d pairing workers", n)
	for i := 0; i < n; i++ {
		go pair()
	}
}
