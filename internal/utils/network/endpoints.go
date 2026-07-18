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
