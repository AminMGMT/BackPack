package transport

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/web"
	"github.com/sirupsen/logrus"
)

// lifecycle is the part of every reverse client transport that owns its runs.
//
// A client runs in generations, like the server it talks to: one control
// channel and the pool and streams that serve it, sharing one context. When the
// control channel is lost the transport ends the generation and dials the next.
// The sequence was written out seven times and the copies had drifted — two of
// them left the pool figures of a connection that was gone on the panel until
// the next run's first tick. It is here once now; a transport supplies only
// what it must tear down besides the control channel, what per-generation state
// it resets, and how it starts.
type lifecycle struct {
	// parentctx is the tunnel's context. Every generation's context derives
	// from it, and a restart finding it done builds nothing.
	parentctx context.Context
	logger    *logrus.Logger
	usage     usageSpec

	// state is the current generation, published whole. See clientState.
	state clientState
	// status is what the panel shows.
	status tunnelStatus

	restartMutex sync.Mutex

	// The pool's size and load, counted for the generation that is running.
	poolConnections    int32
	loadConnections    int32
	dialingConnections int32
	poolDialMutex      sync.Mutex
	// controlFlow carries the server's requests for pool connections.
	controlFlow chan struct{}
}

// usageSpec is what each generation's usage monitor is built from.
type usageSpec struct {
	webPort    int
	snifferLog string
	sniffer    bool
}

// restartPause is how long a restart waits before dialling again, so a server
// that refuses at once is not redialled in a tight loop.
const restartPause = 2 * time.Second

// firstGeneration prepares the lifecycle and installs the first generation the same way
// every later one is installed, so there is only one path that ever writes it.
func (l *lifecycle) firstGeneration(parent context.Context, logger *logrus.Logger, usage usageSpec) {
	l.parentctx = parent
	l.logger = logger
	l.usage = usage
	l.controlFlow = make(chan struct{}, 100)
	ctx, cancel := context.WithCancel(parent)
	l.publish(ctx, cancel)
}

// publish installs a whole new generation at once: a reader must never see the
// new context paired with the old monitor, or the other way round.
func (l *lifecycle) publish(ctx context.Context, cancel context.CancelFunc) {
	l.state.Reset(ctx, cancel, web.NewDataStore(fmt.Sprintf(":%v", l.usage.webPort), ctx, l.usage.snifferLog, l.usage.sniffer, l.status.get, l.logger))
}

// Running reports whether a control channel is up.
func (l *lifecycle) Running() bool { return connected(l.status.get()) }

// restart ends the running generation and, unless the whole tunnel is shutting
// down, starts the next. teardown closes what the transport holds besides the
// control channel (optional); reset clears the transport's own per-generation
// state (optional); start begins the next generation.
func (l *lifecycle) restart(teardown, reset, start func()) {
	if !l.restartMutex.TryLock() {
		l.logger.Warn("client is already restarting")
		return
	}
	defer l.restartMutex.Unlock()

	l.logger.Info("restarting client...")

	// The teardown produces a burst of timeout errors from goroutines that are
	// being told to stop; they say nothing an operator can act on.
	level := l.logger.GetLevel()
	l.logger.SetLevel(logrus.FatalLevel)

	l.state.Stop()
	if teardown != nil {
		teardown()
	}

	l.state.Wait()
	timer := time.NewTimer(restartPause)
	select {
	case <-l.parentctx.Done():
	case <-timer.C:
	}
	timer.Stop()

	// The whole tunnel may have been shut down while this restart was waiting —
	// on a reload, or on the process going down. Rebuilding the run from a
	// parent context that is already finished would dial the server only to
	// hang up again, and on a reload that means fighting the run replacing this
	// one. Nothing here is worth starting.
	if l.parentctx.Err() != nil {
		// The level was turned down to hide the timeouts a teardown produces;
		// leaving it there would silence the shutdown itself.
		l.logger.SetLevel(level)
		// Abandoning is not a reason to keep claiming a peer: this end
		// publishes the status the panel reads and the "connected" flag the
		// watchdog reads, and both used to survive a restart that gave up.
		l.status.set("")
		metrics.ClearPeer()
		l.logger.Debug("restart abandoned: the tunnel is shutting down")
		return
	}

	ctx, cancel := context.WithCancel(l.parentctx)
	l.publish(ctx, cancel)
	l.status.set("")
	if reset != nil {
		reset()
	}
	atomic.StoreInt32(&l.poolConnections, 0)
	atomic.StoreInt32(&l.loadConnections, 0)
	// The published pool figures and the peer belong to the run that just
	// ended. Left behind, the panel would keep showing a connection that is
	// gone until the new run's first tick replaced them.
	metrics.ClearPool()
	metrics.ClearPeer()
	drain(l.controlFlow)

	l.logger.SetLevel(level)

	l.state.Go(start)
}

// beginPoolDial reserves a place before the dial, not after it has connected.
// Control requests and pool repair therefore share the same resource ceiling.
func (l *lifecycle) beginPoolDial(limit int) (func(), bool) {
	l.poolDialMutex.Lock()
	defer l.poolDialMutex.Unlock()
	if l.state.Ctx().Err() != nil || int(atomic.LoadInt32(&l.poolConnections)+atomic.LoadInt32(&l.dialingConnections)) >= max(limit, 1) {
		return nil, false
	}
	atomic.AddInt32(&l.dialingConnections, 1)
	var once sync.Once
	return func() { once.Do(func() { atomic.AddInt32(&l.dialingConnections, -1) }) }, true
}
