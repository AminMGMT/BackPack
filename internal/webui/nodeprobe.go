package webui

import (
	"sync"
	"time"

	"github.com/backpack/backpack/internal/node"
	"github.com/backpack/backpack/internal/utils/network"
)

// What the path between this panel and each managed server is actually like.
//
// The fleet card had a row at the bottom repeating the login it was reached
// with — the user, an at sign and the address, all three of which are on the
// card already or in the form behind it. That space is worth something better:
// the panel runs on the Iran side and every managed server is on the other end
// of the route that matters, so the one thing it can say there that nothing
// else on the screen says is how that route is behaving.
//
// Loss rather than latency alone, because latency on these paths is mostly
// distance and does not change, while loss is what a filtered or congested
// route does first and is what a tunnel over it feels.
//
// Measured here rather than on the server: a figure the far end reports is
// about its own view of the path, and the question being asked is about this
// one.
const (
	// probeEvery is how often the whole fleet is measured. The report asked for
	// five seconds; the measurement itself takes about one, so this is a
	// quarter of the time spent pinging and the rest idle.
	probeEvery = 5 * time.Second

	// probeSamples is how many echoes each round sends. Five is the fewest that
	// can express loss in steps small enough to be worth printing — one lost of
	// five is 20%, which is coarse, but the figure people act on is "nothing"
	// against "something" and five rounds settle that in a second.
	probeSamples = 5
)

// netHealth is one measurement of the path to one server.
//
// Measured is the field that matters. ICMP is blocked outright on plenty of
// hosts and inside plenty of containers, and ping cannot tell that from a
// server that dropped every packet — both come back as 100% loss. Reporting
// that as "100% loss" on a card would be a confident lie about a server that
// is working perfectly, so an unanswered probe is recorded as not measured and
// the card says nothing rather than something false.
type netHealth struct {
	Measured bool    `json:"measured"`
	LossPct  float64 `json:"lossPct"`
	RTTms    float64 `json:"rttMs"`
	JitterMs float64 `json:"jitterMs"`
	At       int64   `json:"at,omitempty"` // unix seconds
}

// nodeProbe holds the last measurement for every server.
//
// Package level, like the panel's other background readings — the addresses
// behind identity(), the update record — so it needs no field on the server
// struct and the handlers' tests construct exactly the server they always did.
var nodeNet = &nodeProbe{seen: map[string]netHealth{}}

type nodeProbe struct {
	mu      sync.Mutex
	seen    map[string]netHealth
	started bool
}

// health returns what is known about the path to one server. A server never
// measured comes back zeroed with Measured false, which is what a card that
// has not been told anything yet should draw.
func (p *nodeProbe) health(name string) netHealth {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[name]
}

// start begins measuring, once per process. Starting twice would double the
// probe rate for no extra information.
func (p *nodeProbe) start() {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.mu.Unlock()

	go p.loop()
}

func (p *nodeProbe) loop() {
	// A first pass immediately, so a panel that has just started has something
	// to show on the first fleet page somebody opens rather than a dash for the
	// first five seconds.
	p.pass()

	t := time.NewTicker(probeEvery)
	defer t.Stop()
	for range t.C {
		p.pass()
	}
}

// pass measures every server in the fleet.
//
// The servers are measured together rather than one after another: done in
// sequence a fleet of six would take six seconds to get through a round that is
// supposed to happen every five, and the last card would always be showing a
// reading older than the first.
func (p *nodeProbe) pass() {
	list := node.List()
	if len(list) == 0 {
		return
	}

	type result struct {
		name string
		h    netHealth
	}
	out := make([]result, len(list))

	var wg sync.WaitGroup
	for i, n := range list {
		wg.Add(1)
		go func(i int, name, host string) {
			defer wg.Done()
			s := network.ScoreEndpoint(host, probeSamples)
			out[i] = result{name: name, h: netHealth{
				Measured: s.Reachable,
				LossPct:  s.LossPct,
				RTTms:    s.RTTms,
				JitterMs: s.JitterMs,
				At:       time.Now().Unix(),
			}}
		}(i, n.Name, n.Host)
	}
	wg.Wait()

	p.mu.Lock()
	defer p.mu.Unlock()
	// Rebuilt rather than merged, so a server that has left the fleet stops
	// being remembered here too.
	fresh := make(map[string]netHealth, len(out))
	for _, r := range out {
		// An unanswered round keeps the previous measurement rather than
		// blanking the card: one dropped probe on an otherwise good path is a
		// worse thing to show than a reading a few seconds old.
		if !r.h.Measured {
			if prev, ok := p.seen[r.name]; ok && prev.Measured {
				fresh[r.name] = prev
				continue
			}
		}
		fresh[r.name] = r.h
	}
	p.seen = fresh
}
