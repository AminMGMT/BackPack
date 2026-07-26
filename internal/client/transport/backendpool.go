package transport

import (
	"net"
	"strings"
	"sync"
	"time"
)

// Backend pool: health-checked load balancing across several local services.
//
// A forwarded port can name more than one backend, separated by a pipe
// (`443=127.0.0.1:8443|127.0.0.1:8444`). Connections are spread over the
// backends that are up, and a backend that stops answering is dropped from the
// rotation until it recovers — so one dead service behind the tunnel no longer
// takes the tunnel down with it.
//
// The separator is a pipe rather than a comma because a comma already separates
// whole port entries ("443,8080"), so a comma here would split one mapping into
// two.
//
// A single backend (no pipe) is returned unchanged and never health-checked,
// so every existing tunnel behaves exactly as before: this is inert until a
// second backend is configured.
//
// The check is a plain TCP connect (item 10's active health check) and the
// pick is round-robin over the healthy set (item 9's load balancing) — the two
// are the same mechanism here, because the check is what decides the set the
// balancer draws from.

// backendSep separates several backends inside one port mapping.
const backendSep = "|"

const (
	backendCheckInterval = 10 * time.Second
	backendProbeTimeout  = 3 * time.Second
	backendMaxFailed     = 3 // consecutive failures before a backend is dropped
)

var backends = &backendPool{groups: map[string]*backendGroup{}}

type backendPool struct {
	mu     sync.Mutex
	groups map[string]*backendGroup
}

// pick returns a backend to dial for this target. For a single backend it is a
// no-op passthrough; for several it hands out the next healthy one.
func (p *backendPool) pick(target string) string {
	if !strings.Contains(target, backendSep) {
		return target
	}
	return p.group(target).pick()
}

// group returns the pool for a backend list, creating it (and its background
// health checker) the first time the list is seen.
func (p *backendPool) group(target string) *backendGroup {
	p.mu.Lock()
	defer p.mu.Unlock()
	if g, ok := p.groups[target]; ok {
		return g
	}
	list := splitBackends(target)
	g := &backendGroup{
		list:    list,
		healthy: make([]bool, len(list)),
		fails:   make([]int, len(list)),
	}
	for i := range g.healthy {
		g.healthy[i] = true // optimistic until the first check proves otherwise
	}
	p.groups[target] = g
	go g.run()
	return g
}

type backendGroup struct {
	list    []string
	mu      sync.Mutex
	healthy []bool
	fails   []int
	next    int
}

// pick returns the next healthy backend, round-robin. If none are healthy it
// still returns one (best effort) rather than dropping the connection — the
// dial will fail and be reported, which is more useful than silence.
func (g *backendGroup) pick() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := len(g.list)
	for i := 0; i < n; i++ {
		idx := g.next % n
		g.next++
		if g.healthy[idx] {
			return g.list[idx]
		}
	}
	idx := g.next % n
	g.next++
	return g.list[idx]
}

// run probes every backend on a timer, dropping one after backendMaxFailed
// consecutive failures and restoring it the moment it answers again.
func (g *backendGroup) run() {
	probe := backendProbe
	for {
		for i, b := range g.list {
			ok := probe(b)
			g.mu.Lock()
			if ok {
				g.healthy[i], g.fails[i] = true, 0
			} else {
				g.fails[i]++
				if g.fails[i] >= backendMaxFailed {
					g.healthy[i] = false
				}
			}
			g.mu.Unlock()
		}
		time.Sleep(backendCheckInterval)
	}
}

// backendProbe is a plain TCP connect; a variable so tests can substitute it.
var backendProbe = func(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, backendProbeTimeout)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// firstBackend returns the first address of a backend list, or the input
// unchanged. Used by the UDP transport, which cannot be health-checked with a
// TCP probe, so it does not load-balance — it just tolerates a list.
func firstBackend(target string) string {
	if b := splitBackends(target); len(b) > 0 {
		return b[0]
	}
	return target
}

// splitBackends parses a backend list into trimmed, non-empty addresses.
func splitBackends(list string) []string {
	var out []string
	for _, p := range strings.Split(list, backendSep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
