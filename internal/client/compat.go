package client

import (
	"context"
	"github.com/backpack/backpack/internal/plain/core"
	"sync"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/utils/network"
)

// A tunnel whose two servers are updated one at a time — the kharej's half.
// See the server's compat.go for the whole of it.
//
// The kharej always tries the plain engine first. An Iran server still on
// Backpack v2.0.0 refuses that handshake in a way it can tell (too old over
// tcp, its decoy page over ws), and the tunnel then runs on the v2.0.0 engine.
// When that connection drops — the Iran server was restarted, which updating
// it does — the plain engine is tried again first, so an updated pair ends up
// on it without anyone asking.

const compatRetry = 15 * time.Second

type compatRunner struct {
	mu  sync.Mutex
	cur runner
}

func (c *compatRunner) Running() bool {
	c.mu.Lock()
	r := c.cur
	c.mu.Unlock()
	return r != nil && r.Running()
}

func (c *Client) startCompat(ctx context.Context, tr config.TransportType, endpoints *network.Endpoints, outbound *network.Outbound) runner {
	// A candidate whose run has already ended starts nothing.
	if ctx.Err() != nil {
		return nil
	}
	cr := &compatRunner{}
	go func() {
		legacy := false
		for ctx.Err() == nil {
			run, stop := context.WithCancel(ctx)
			found := make(chan struct{}, 1)
			var r runner
			if legacy {
				r = c.startEngine(run, tr, endpoints, outbound)
			} else {
				r = c.startPlain(run, tr, &core.Route{Endpoints: endpoints, Outbound: outbound}, func() {
					select {
					case found <- struct{}{}:
					default:
					}
				})
			}
			cr.mu.Lock()
			cr.cur = r
			cr.mu.Unlock()

			if legacy {
				// Back to the plain engine as soon as this connection drops —
				// an Iran server being updated restarts, and the plain engine
				// has to be the first thing the updated one hears, or it would
				// move to v2.0.0's engine to serve this one and the pair would
				// stay there. A connection that never comes up is given
				// compatRetry before the plain engine is tried again.
				started, wasUp := time.Now(), false
				tick := time.NewTicker(250 * time.Millisecond)
				for ctx.Err() == nil {
					<-tick.C
					if r.Running() {
						wasUp = true
						continue
					}
					if wasUp || time.Since(started) >= compatRetry {
						break
					}
				}
				tick.Stop()
				legacy = false
				if ctx.Err() == nil {
					c.logger.Infof("tunnel on %s: trying the plain engine again", tr)
				}
			} else {
				select {
				case <-ctx.Done():
				case <-found:
					legacy = true
					c.logger.Infof("tunnel on %s: the Iran server runs Backpack v2.0.0 — using its engine until it is updated", tr)
				}
			}
			stop()
			if ctx.Err() == nil {
				// The old run's goroutines are ending; give them a moment.
				time.Sleep(time.Second)
			}
		}
	}()
	return cr
}
