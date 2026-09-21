// Package chain runs an ordered list of transport candidates, one at a time,
// and moves on from one that does not come up.
//
// The problem it solves is the one the product exists for. A tunnel is pinned
// to a single transport, and when that transport is the one being filtered the
// tunnel retries it for ever: the operator is the failover mechanism. A chain
// makes the tunnel try the next carrier by itself.
//
// # Why one at a time
//
// Listening on every candidate at once would be the obvious design and it does
// not work here. A reverse-tunnel server binds the forwarded ports as part of
// starting a transport, so two live candidates fight over the same ports and
// the second one loses. Running them in sequence sidesteps that completely and
// costs nothing else: only one candidate can be carrying traffic anyway.
//
// # How the two ends meet
//
// Both ends walk the same configured list, and neither tells the other where it
// is. They meet because the client sweeps faster than the server dwells: the
// server holds each candidate for Dwell, and the client gives each candidate
// Dwell/len(candidates), so the client tries every candidate at least once
// inside one server dwell. Rendezvous therefore takes at most one dwell, and
// needs no negotiation, no shared clock and no wire change.
//
// Once a candidate settles, rotation stops. This is deliberately a better first
// connect and not live switching: there is no session migration here, and a
// transport that is up keeps its own reconnect behaviour. Rotation resumes only
// if a settled candidate stays down for Regrace, which is the case where the
// carrier really has been taken away.
package chain

import (
	"context"
	"time"
)

// Attempt is what a caller hands back when the chain asks it to start a
// candidate. Settled reports whether the tunnel's control channel is up; the
// chain polls it and never blocks on it.
type Attempt struct {
	Settled func() bool
}

// Starter launches one candidate under ctx. Cancelling ctx must tear it down.
type Starter func(ctx context.Context, transport string) Attempt

// Chain is the supervisor. Build it with New and run it once.
type Chain struct {
	candidates []string
	dwell      time.Duration
	regrace    time.Duration
	// teardown is how long to wait after cancelling a candidate before binding
	// the next one, so the ports the last one held are actually free.
	teardown time.Duration
	// hooks, all optional
	log      func(string)
	remember func(string)

	// poll is the settle-check interval and windowFloor is the shortest an
	// attempt may be. Both are fields rather than constants so the tests can
	// drive the whole state machine in milliseconds.
	poll        time.Duration
	windowFloor time.Duration
}

// New builds a chain over primary followed by fallbacks, with duplicates and
// blanks removed so a list that repeats the primary behaves as written.
//
// dwell is how long the server holds one candidate. A client passes the same
// dwell and the chain divides it internally — see the package comment — so both
// ends are configured with one number that means the same thing.
func New(primary string, fallbacks []string, dwell time.Duration) *Chain {
	c := &Chain{
		dwell:    dwell,
		regrace:  5 * time.Minute,
		teardown: 3 * time.Second,
		poll:     250 * time.Millisecond,
		// A window shorter than a dial timeout would reject candidates for
		// being slow rather than for being blocked, which is the very failure
		// this mechanism exists to stop making worse.
		windowFloor: 5 * time.Second,
	}
	seen := map[string]bool{}
	for _, name := range append([]string{primary}, fallbacks...) {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		c.candidates = append(c.candidates, name)
	}
	return c
}

// OnLog sends the chain's decisions somewhere. Without it the chain is silent,
// which is wrong for an operator watching a tunnel move.
func (c *Chain) OnLog(f func(string)) *Chain { c.log = f; return c }

// OnSettled is called with the candidate that came up, so a caller can record
// it and start there next time.
func (c *Chain) OnSettled(f func(string)) *Chain { c.remember = f; return c }

// Candidates returns the list in the order it will be tried.
func (c *Chain) Candidates() []string { return append([]string(nil), c.candidates...) }

// Single reports whether there is nothing to fall back to, in which case Run is
// exactly equivalent to starting that one transport and waiting. Callers use it
// to skip the rotation machinery entirely on the default configuration.
func (c *Chain) Single() bool { return len(c.candidates) <= 1 }

func (c *Chain) logf(s string) {
	if c.log != nil {
		c.log(s)
	}
}

// attemptWindow is how long one candidate gets. The server holds a candidate
// for the whole dwell; a client divides the dwell by the number of candidates
// so that it sweeps the list inside one server dwell. Both are derived from the
// same configured number, which is why only one is configured.
func (c *Chain) attemptWindow(sweep bool) time.Duration {
	if !sweep || len(c.candidates) < 2 {
		return c.dwell
	}
	w := c.dwell / time.Duration(len(c.candidates))
	if w < c.windowFloor {
		return c.windowFloor
	}
	return w
}

// Run rotates until ctx is done. sweep selects the client's faster cadence; a
// server passes false.
//
// It returns only when ctx is done, so callers treat it the way they treated
// the blocking wait it replaces.
func (c *Chain) Run(ctx context.Context, sweep bool, start Starter) {
	if len(c.candidates) == 0 {
		<-ctx.Done()
		return
	}
	window := c.attemptWindow(sweep)
	for i := 0; ctx.Err() == nil; i++ {
		name := c.candidates[i%len(c.candidates)]
		if len(c.candidates) > 1 {
			c.logf("trying transport " + name)
		}
		// Whether the candidate never came up or came up and was later taken
		// away, the answer is the same: try the next one. The list wraps, so a
		// carrier that was blocked an hour ago is tried again a sweep later
		// without needing a rule of its own.
		c.attempt(ctx, name, window, start)
	}
}

// attempt runs one candidate and returns when it is time to try another.
func (c *Chain) attempt(parent context.Context, name string, window time.Duration, start Starter) {
	ctx, cancel := context.WithCancel(parent)
	// Cancelling is not instant: the candidate's listeners have to notice and
	// let go of their ports before the next candidate asks for them.
	defer func() {
		cancel()
		if len(c.candidates) > 1 {
			select {
			case <-time.After(c.teardown):
			case <-parent.Done():
			}
		}
	}()

	at := start(ctx, name)
	if at.Settled == nil {
		// Nothing to watch — the caller does not distinguish states, so this
		// candidate owns the tunnel until the whole thing stops.
		<-ctx.Done()
		return
	}

	ticker := time.NewTicker(c.poll)
	defer ticker.Stop()

	deadline := time.After(window)
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			if len(c.candidates) == 1 {
				// Nowhere to go; stop watching and let it keep retrying.
				<-ctx.Done()
				return
			}
			c.logf("transport " + name + " did not come up, moving on")
			return
		case <-ticker.C:
			if !at.Settled() {
				continue
			}
			c.logf("transport " + name + " is up")
			if c.remember != nil {
				c.remember(name)
			}
			c.hold(ctx, name, at)
			return
		}
	}
}

// hold stays on a settled candidate. It gives up only when the candidate has
// been continuously down for regrace — a long time on purpose, because every
// transport already reconnects on its own and rotating through a transient drop
// would replace a short outage with a longer one.
func (c *Chain) hold(ctx context.Context, name string, at Attempt) {
	ticker := time.NewTicker(c.poll)
	defer ticker.Stop()

	var downSince time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if at.Settled() {
				downSince = time.Time{}
				continue
			}
			if downSince.IsZero() {
				downSince = time.Now()
				continue
			}
			if time.Since(downSince) >= c.regrace {
				c.logf("transport " + name + " has been down for " +
					c.regrace.String() + ", trying the other transports again")
				return
			}
		}
	}
}
