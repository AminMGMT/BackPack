package network

import (
	"strings"
	"sync/atomic"
	"time"
)

// Endpoints is an ordered, rotating list of server addresses a client can dial.
//
// It exists because a single address is a single point of failure: the server's
// IP may be filtered from the client's network while another IP, another port,
// or a CDN edge of the same server still works. The control-channel loop calls
// Rotate() every time a connection attempt fails, so the client walks the list
// until something connects — and every data connection then uses whichever
// endpoint is currently live.
//
// A one-element list behaves exactly like a plain address, so existing tunnels
// are unaffected.
type Endpoints struct {
	list []string
	idx  atomic.Int64
	// spreadIdx is a second, independent cursor used by Next() so that
	// balancing data connections never disturbs which endpoint the control
	// channel is pinned to.
	spreadIdx atomic.Int64
	spread    atomic.Bool

	// steer, when set, hands both Current() and Next() over to a health scorer
	// (see healthscore.go): preferred holds the index of the endpoint currently
	// scoring healthiest, so traffic concentrates on the best exit rather than
	// following the dial order. It is mutually exclusive with spread — steering
	// picks one exit on purpose, where spread deliberately uses all of them.
	steer     atomic.Bool
	preferred atomic.Int64
}

// NewEndpoints builds the list from a primary address plus optional fallbacks,
// trimming blanks and dropping duplicates while preserving order.
func NewEndpoints(primary string, fallbacks ...string) *Endpoints {
	e := &Endpoints{}
	seen := map[string]bool{}
	add := func(a string) {
		a = strings.TrimSpace(a)
		if a == "" || seen[a] {
			return
		}
		seen[a] = true
		e.list = append(e.list, a)
	}
	add(primary)
	for _, f := range fallbacks {
		add(f)
	}
	return e
}

// Current returns the endpoint that should be dialled right now.
func (e *Endpoints) Current() string {
	if e == nil || len(e.list) == 0 {
		return ""
	}
	if e.steer.Load() {
		return e.list[e.clampIdx(e.preferred.Load())]
	}
	i := int(e.idx.Load()) % len(e.list)
	return e.list[i]
}

// clampIdx wraps a stored index into range, guarding against a negative wrap
// after a very long uptime.
func (e *Endpoints) clampIdx(v int64) int {
	i := int(v) % len(e.list)
	if i < 0 {
		i += len(e.list)
	}
	return i
}

// Rotate advances to the next endpoint and returns it. With a single endpoint
// it is a no-op, so simple setups never change behaviour.
func (e *Endpoints) Rotate() string {
	if e == nil || len(e.list) <= 1 {
		return e.Current()
	}
	// Under health steering a failed dial means the preferred exit is bad right
	// now: step off it and let the next scoring cycle settle on a new best,
	// which will avoid this one because it just failed to answer.
	if e.steer.Load() {
		e.preferred.Store(int64(e.clampIdx(e.preferred.Load() + 1)))
		return e.Current()
	}
	e.idx.Add(1)
	return e.Current()
}

// Next returns the endpoint a *new* data connection should use, advancing a
// separate cursor each call so the pool spreads itself across every endpoint
// instead of piling onto one.
//
// This is what turns a fallback list into load balancing and multipath: with
// spread enabled the pool ends up holding connections over several addresses at
// once, so one throttled or congested route only slows the share of traffic
// riding on it rather than the whole tunnel.
//
// The control channel deliberately keeps using Current(): it must stay on one
// endpoint, because it is the connection the server identifies the peer by.
//
// With spread disabled — or a single endpoint — this is exactly Current(), so
// existing tunnels behave as before.
func (e *Endpoints) Next() string {
	if e == nil || len(e.list) == 0 {
		return ""
	}
	// Steering wins over spread: when it is on, every new data connection also
	// rides the endpoint currently scoring healthiest.
	if e.steer.Load() {
		return e.list[e.clampIdx(e.preferred.Load())]
	}
	if !e.spread.Load() || len(e.list) == 1 {
		return e.Current()
	}
	i := int(e.spreadIdx.Add(1)-1) % len(e.list)
	if i < 0 { // guard against a wrapped counter after a very long uptime
		i = 0
	}
	return e.list[i]
}

// SetSpread turns load balancing across endpoints on or off.
func (e *Endpoints) SetSpread(on bool) {
	if e == nil {
		return
	}
	e.spread.Store(on)
}

// Spread reports whether data connections are being spread across endpoints.
func (e *Endpoints) Spread() bool {
	return e != nil && e.spread.Load()
}

// Len reports how many endpoints are configured.
func (e *Endpoints) Len() int {
	if e == nil {
		return 0
	}
	return len(e.list)
}

// All returns the configured endpoints in order.
func (e *Endpoints) All() []string {
	if e == nil {
		return nil
	}
	out := make([]string, len(e.list))
	copy(out, e.list)
	return out
}

// InPreferenceOrder returns every endpoint, starting from the one currently
// preferred and wrapping round.
//
// It is what a racer wants and what Current/Rotate cannot give: the full list,
// but ordered so the address this tunnel has been using — or was steered onto
// by health scoring — is still tried first and given its head start. A racer
// handed the raw list would restart from the primary on every reconnect and
// undo whatever the failover had decided.
func (e *Endpoints) InPreferenceOrder() []string {
	if e == nil {
		return nil
	}
	all := e.All()
	if len(all) < 2 {
		return all
	}
	// Start from whichever endpoint Current would hand back, so steering and
	// rotation both survive into the race. Reading the cursor directly would
	// ignore health steering, which is the one thing that has an opinion about
	// which exit is best right now.
	start := 0
	if cur := e.Current(); cur != "" {
		for i, a := range all {
			if a == cur {
				start = i
				break
			}
		}
	}

	out := make([]string, 0, len(all))
	for i := 0; i < len(all); i++ {
		out = append(out, all[(start+i)%len(all)])
	}
	return out
}

// RaceStagger is how long each endpoint waits behind the one before it.
//
// Short enough that a dead address costs a fraction of a dial timeout rather
// than all of it, long enough that an address which is merely a little slow
// still wins on its own — a TCP handshake across a continent is tens of
// milliseconds, and racing past one at 50ms would open a second connection on
// every reconnect for no reason.
const RaceStagger = 300 * time.Millisecond

// Prefer pins the list to a named address, if it holds one.
//
// It is what a caller that raced the list calls afterwards: the race decided
// which server is reachable right now, and everything that follows — the data
// connections, the pool, the next reconnect — should go to the same one rather
// than starting again from the primary.
//
// A no-op under health steering. That scorer measures every endpoint on a timer
// and concentrates traffic on the best one on purpose; letting a single
// successful dial override it would replace a measurement with an accident.
func (e *Endpoints) Prefer(addr string) {
	if e == nil || addr == "" || len(e.list) < 2 || e.steer.Load() {
		return
	}
	for i, a := range e.list {
		if a == addr {
			e.idx.Store(int64(i))
			return
		}
	}
}
