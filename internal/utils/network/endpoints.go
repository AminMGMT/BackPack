package network

import (
	"strings"
	"sync/atomic"
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
	i := int(e.idx.Load()) % len(e.list)
	return e.list[i]
}

// Rotate advances to the next endpoint and returns it. With a single endpoint
// it is a no-op, so simple setups never change behaviour.
func (e *Endpoints) Rotate() string {
	if e == nil || len(e.list) <= 1 {
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
