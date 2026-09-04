package webui

import (
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/node"
)

// What the far end says about the last hop.
//
// A reverse tunnel's forwarded service lives on the machine the panel is not
// running on. When nothing is listening there, every connection is delivered
// across a healthy tunnel and refused one step past the end of it — and this
// side sees a tunnel that is up, a peer that is connected, and counters that
// move. It is right about all three. The reading it is missing is on the other
// machine, and until now the only way to it was to open that machine's journal.
//
// Managed servers are reachable, so it is asked for. The cost is what has to be
// controlled: the tunnels page polls, and a call per tunnel per poll would put
// an SSH round trip on every card every few seconds. So the answer is cached
// for far longer than the poll interval, in the same way and for the same
// reason the runner caches reachability.

// farServiceTTL is how long one server's answer stands for.
//
// A service that has gone down stays down until somebody fixes it, and one that
// has been fixed is worth seeing quickly — but not at the cost of an SSH round
// trip per card per poll. A minute is short enough that the operator sees their
// fix land while they are still looking, and long enough that a page left open
// costs one call a minute per server rather than one per second.
const farServiceTTL = time.Minute

type farServiceCache struct {
	mu   sync.Mutex
	seen map[string]farServiceEntry // keyed by server name
}

type farServiceEntry struct {
	// byTunnel holds the far end's sentence for each tunnel it has one for.
	// A tunnel that is fine is simply absent.
	byTunnel map[string]string
	when     time.Time
	inFlight bool
}

var farService = &farServiceCache{seen: map[string]farServiceEntry{}}

// lookup returns what the given server last said about one tunnel, and asks it
// again in the background when the answer is stale.
//
// It never blocks the caller on the network. A poll that waited for an SSH
// round trip would make the whole tunnels page as slow as the slowest server in
// the fleet, and a server that has gone away would stall it entirely — which is
// the opposite of what this is for.
func (c *farServiceCache) lookup(run node.Runner, server, tunnel string) string {
	if run == nil || server == "" {
		return ""
	}
	c.mu.Lock()
	e, ok := c.seen[server]
	stale := !ok || time.Since(e.when) > farServiceTTL
	if stale && !e.inFlight {
		e.inFlight = true
		c.seen[server] = e
		go c.refresh(run, server)
	}
	said := e.byTunnel[tunnel]
	c.mu.Unlock()
	return said
}

// refresh asks one server about all of its tunnels at once.
//
// One call for the whole server rather than one per tunnel: the far end already
// answers with its full list, and asking per tunnel would multiply the round
// trips by exactly the thing this is trying not to do.
func (c *farServiceCache) refresh(run node.Runner, server string) {
	var states []node.TunnelState
	err := run.Call(server, node.OpList, nil, &states)

	byTunnel := map[string]string{}
	if err == nil {
		for _, st := range states {
			if s := strings.TrimSpace(st.ServiceDown); s != "" {
				byTunnel[st.Name] = s
			}
		}
	}

	c.mu.Lock()
	// A server that could not be answered is recorded as having said nothing,
	// with the time set, so a fleet that is down is asked once a minute rather
	// than on every poll. Being unreachable is already reported elsewhere; it
	// must not also become a claim about the service behind the tunnel.
	c.seen[server] = farServiceEntry{byTunnel: byTunnel, when: time.Now()}
	c.mu.Unlock()
}

// forget drops one server's answers, so the next look asks again. Used when a
// server leaves the fleet or its login changes.
func (c *farServiceCache) forget(server string) {
	c.mu.Lock()
	delete(c.seen, server)
	c.mu.Unlock()
}
