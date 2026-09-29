package transport

import (
	"context"
	"io"
	"net"
	"sort"
	"sync"
	"time"
)

// A generation serves one client at a time, and the client can change without
// the generation ending.
//
// A generation is the tunnel port, the forwarded ports and the workers that
// pair users with tunnel connections. A client is a control channel, the loop
// that serves it, the pool nonce it was given and the pool connections it has
// opened. They used to live and die together: a second control claim, or any
// failure on the control channel, restarted the whole generation — the tunnel
// port and every forwarded port were closed and bound again, and every user
// connection through them was cut. On the links these tunnels run over, where
// the path between the two servers drops for a second at a time, that happened
// all day: each blip was a restart, and a kharej re-dialling while the Iran
// side had not yet noticed its old channel was dead was a restart too. With two
// kharej holding one token it never stopped. Reported from several servers on
// v1.8.4, where the fix that circulated rewrote the re-claim to swap the
// channel in place; this does that for every transport, and for the failure
// path as well.
//
// So the seat is per generation, and the client in it is replaced in place:
//
//   - A claim that proves the token while the generation is serving is seated
//     at once. Whoever held the seat is ended — its loop says goodbye on its own
//     channel and closes it — and its pool connections are dropped, since they
//     belong to a client that has gone.
//   - A control channel that fails empties the seat and leaves the generation
//     running. The next claim is seated the same way.
//
// Only a failure of the generation itself — the tunnel port — restarts it.
type clientSeat struct {
	mu sync.Mutex
	// open is set once the generation is serving; before that the first claim
	// goes through the generation's own handshake, which is what starts it.
	open bool
	// epoch identifies the seated client, so a loop that has been replaced
	// cannot empty the seat of the client that replaced it.
	epoch uint64
	// end ends the seated client's control loop; nil when the seat is empty.
	end context.CancelFunc
	// ctx is the seated client's context: it ends when the client is replaced
	// or its channel fails. What the client opened — its mux sessions — runs on
	// it, so nothing of a gone client outlives it. Nil when the seat is empty.
	ctx context.Context
}

// client is the context of the client seated now, for whatever it opens. An
// empty seat gives one that has already ended: a session arriving between
// clients belongs to neither.
func (c *clientSeat) client() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx != nil {
		return c.ctx
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// serving reports whether a claim is to be seated directly.
func (c *clientSeat) serving() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.open
}

// sit seats a new client. It ends the loop of whoever held the seat and calls
// vacate for it; install then puts the new client's channel in place — the
// transport's nonce, control channel and peer — and run starts its control
// loop on a context of its own, whose lost function empties the seat instead
// of restarting the generation.
//
// Every change of seat happens under one lock, vacate and install included, so
// a loop that fails just as its replacement is seated cannot clear the new
// client's channel. Neither may call back into the seat.
func (c *clientSeat) sit(genCtx context.Context, vacate, install func(), run func(ctx context.Context, lost func())) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if genCtx.Err() != nil {
		return
	}
	if c.end != nil {
		c.end()
		vacate()
	}
	c.epoch++
	epoch := c.epoch
	ctx, cancel := context.WithCancel(genCtx)
	c.end = cancel
	c.ctx = ctx
	c.open = true
	install()

	lost := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.epoch != epoch || c.end == nil {
			return // replaced already, or the seat was emptied
		}
		c.end = nil
		c.ctx = nil
		cancel()
		if genCtx.Err() == nil {
			vacate()
		}
	}
	go run(ctx, lost)
}

// drainTunnelConns closes whatever pool connections are queued, without
// waiting for more.
func drainTunnelConns[C io.Closer](queue chan C) {
	for {
		select {
		case c := <-queue:
			c.Close()
		default:
			return
		}
	}
}

// rivalry watches who takes the seat, to tell one client re-dialing — which is
// what adoption is for — from two clients holding one token, which it cannot
// fix: each one's claim ends the other's, and with adoption in place the
// tunnel changes hands every few seconds instead of restarting, which is
// quieter and no more use. Only the log can say so.
type rivalry struct {
	mu     sync.Mutex
	recent []seatEvent
	warned time.Time
}

type seatEvent struct {
	host string
	at   time.Time
}

// rivalWindow is how far back seats are counted, and rivalSeats how many
// changes of hands in it, between at least two hosts, make a rivalry rather
// than a flaky path.
const (
	rivalWindow = time.Minute
	rivalSeats  = 4
	rivalRepeat = 5 * time.Minute
)

// seat records a seating from addr at now, and reports the hosts involved when
// they are taking turns — at most once per rivalRepeat.
func (r *rivalry) seat(addr string, now time.Time) ([]string, bool) {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recent = append(r.recent, seatEvent{host, now})
	kept := r.recent[:0]
	for _, e := range r.recent {
		if now.Sub(e.at) <= rivalWindow {
			kept = append(kept, e)
		}
	}
	r.recent = kept
	changes, hosts := 0, map[string]bool{}
	for i, e := range r.recent {
		hosts[e.host] = true
		if i > 0 && r.recent[i-1].host != e.host {
			changes++
		}
	}
	if changes < rivalSeats || len(hosts) < 2 || now.Sub(r.warned) < rivalRepeat {
		return nil, false
	}
	r.warned = now
	names := make([]string, 0, len(hosts))
	for h := range hosts {
		names = append(names, h)
	}
	sort.Strings(names)
	return names, true
}
