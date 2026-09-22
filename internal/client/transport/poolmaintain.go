package transport

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/backpack/backpack/internal/metrics"
)

// How big the connection pool should be, decided once.
//
// Every client transport keeps a pool of connections dialled ahead of the
// traffic that will use them, and every one of them had its own copy of the
// loop that sizes it. The copies were the same loop — the same two tickers, the
// same two factors, the same growth and shrink conditions — and they had
// drifted exactly the way copies do.
//
// Three of the seven were still character-for-character identical. The other
// four had each been edited at a different time, and the drift was not
// cosmetic:
//
//   - **`udp` never reported its pool at all**, so the panel's connection-pool
//     card was simply absent on a udp tunnel — not empty, not zero, missing —
//     while it appeared for every other transport.
//   - **`udp` never grew its pool on throughput either.** The load signal that
//     lets a pool grow when its connections are each working hard, rather than
//     only when somebody is waiting for one, was added to the others and not to
//     it. A udp tunnel under sustained load therefore sat at its configured
//     size while the same load grew every other transport's pool.
//   - `ws` had already been through the first of those two, and the comment
//     recording it is still in the history: the card "was simply missing on a
//     ws or wss tunnel". It was fixed there and not looked for anywhere else.
//
// That is the whole argument for one copy. The policy here is a judgement about
// when a tunnel needs more connections, and a judgement that exists seven times
// is seven judgements.
//
// # What a transport still decides
//
// How to dial one connection, and what a connection is. `dial` is the only
// thing passed in, and every difference between the transports lives behind it.

// poolSizer is everything the sizing loop needs from the transport that owns
// the pool.
//
// The counters are pointers because they belong to the transport and are
// written by its dialler and its handler, on other goroutines; this loop only
// reads and resets them.
type poolSizer struct {
	ctx context.Context
	log *logrus.Logger

	// size is the configured pool size — the floor the pool returns to, and
	// the figure the panel shows beside what is actually open.
	size int
	// aggressive selects the tighter factors: grow sooner, shrink later.
	aggressive bool

	// open counts connections sitting in the pool right now.
	open *int32
	// taken counts connections handed to traffic since the last load tick.
	taken *int32

	// shrink is the channel the transport listens on to retire one connection.
	shrink chan struct{}
	// dial starts one new pool connection. It is expected to return when that
	// connection ends, so the loop starts it on its own goroutine.
	dial func()
}

// maintain fills the pool and then keeps it the right size until ctx ends.
func (p poolSizer) maintain() {
	for i := 0; i < p.size; i++ { // initial pool filling
		go p.dial()
	}

	// The factors. a and b decide when the pool is too small, x and y when it
	// is too large; aggressive tightens both so a burst is met sooner and the
	// pool takes longer to give the connections back.
	a, b := 4, 5
	x, y := 3, 4.0
	if p.aggressive {
		p.log.Info("aggressive pool management enabled")
		a, b = 1, 2
		x, y = 0, 0.75
	}

	tickerPool := time.NewTicker(time.Second * 1)
	defer tickerPool.Stop()

	tickerLoad := time.NewTicker(time.Second * 10)
	defer tickerLoad.Stop()

	newPoolSize := p.size // initial value
	var load poolLoad     // throughput signal, see poolload.go
	var openSum int32

	for {
		select {
		case <-p.ctx.Done():
			return

		case <-tickerPool.C:
			// Accumulate pool connections over time (every second)
			atomic.AddInt32(&openSum, atomic.LoadInt32(p.open))

		case <-tickerLoad.C:
			// The load over the last ten seconds, and the average pool size
			// over the same window. +9 before the divide is a ceiling: a pool
			// that was needed at all should not round down to "not needed".
			taken := (int(atomic.LoadInt32(p.taken)) + 9) / 10
			atomic.StoreInt32(p.taken, 0)

			openAvg := (int(atomic.LoadInt32(&openSum)) + 9) / 10
			atomic.StoreInt32(&openSum, 0)

			// Throughput carried since the previous tick. A pool whose
			// connections are each working hard should grow even when nobody
			// is asking for new ones — see poolload.go.
			// It is the same figure for every transport because there is one
			// process per tunnel and every one of them counts its bytes into
			// the same place — through CountedConn or through AddBytes.
			mbps := load.mbps()

			// The pool is allowed to outgrow its configured size, which from
			// outside is indistinguishable from a leak. Publish what it is
			// doing and why, so the panel can say "8 configured, 19 open,
			// carrying 240 Mbit/s" instead of leaving somebody to guess.
			metrics.ReportPool(openAvg, newPoolSize, p.size, mbps)

			grow := ((taken+a) > openAvg*b && poolCanGrow(newPoolSize, p.size)) ||
				load.wantsMore(mbps, openAvg, newPoolSize, p.size)

			switch {
			case grow:
				p.log.Debugf("increasing pool size: %d -> %d, avg pool conn: %d, avg load conn: %d, throughput: %d Mbit/s",
					newPoolSize, newPoolSize+1, openAvg, taken, mbps)
				newPoolSize++
				go p.dial()

			case float64(taken+x) < float64(openAvg)*y && newPoolSize > p.size:
				p.log.Debugf("decreasing pool size: %d -> %d, avg pool conn: %d, avg load conn: %d",
					newPoolSize, newPoolSize-1, openAvg, taken)
				newPoolSize--
				p.shrink <- struct{}{}
			}
		}
	}
}
