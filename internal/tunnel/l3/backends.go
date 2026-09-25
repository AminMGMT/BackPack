package l3

import (
	"context"
	"errors"
	"net"
	"sort"
	"sync/atomic"
	"time"
)

// Choosing which backend a forwarded connection goes to.
//
// A mapping with several backends is how one Iran server spreads a port over
// several kharej servers: each kharej has its own layer-3 tunnel, the kernel
// routes each peer address over its own interface, and the mapping lists them
// all ("443=10.10.0.2:443|10.10.1.2:443"). Two things decide how well that
// works, and round robin got both wrong.
//
// Bandwidth. Connections are not equal — one download can outweigh a hundred
// page loads — so taking turns leaves one kharej saturated while another idles.
// Each new connection goes to the member with the fewest connections open now,
// which is what spreads the load that is actually there.
//
// A member that has died. Over a layer-3 tunnel whose far end is gone the
// kernel still routes into the interface, so a dial is not refused — it hangs
// until it times out. Round robin sent every n-th connection into that hang,
// for the full ten seconds, for as long as the member stayed down. A member
// whose dial fails is now set aside for backendCooldown and only tried again
// when that has passed or when nothing else answers, and while there is
// another member left to try a dial gives up after memberDialTimeout.

const (
	// memberDialTimeout bounds a dial while there are other members to fall
	// back on. Long enough for a SYN to be retransmitted once over a slow
	// path; the last member left gets the full forwardDialTimeout.
	memberDialTimeout = 3 * time.Second

	// backendCooldown is how long a member whose dial failed is passed over.
	backendCooldown = 20 * time.Second
)

// backendPool is one mapping's backends.
type backendPool struct {
	members []*backendMember
	turn    atomic.Uint64 // rotates the choice among equally loaded members
	now     func() time.Time
}

type backendMember struct {
	addr      string
	active    atomic.Int64 // connections or flows open through it now
	downUntil atomic.Int64 // unix nanoseconds; zero when it is answering
}

func newBackendPool(targets []string) *backendPool {
	p := &backendPool{now: time.Now}
	for _, t := range targets {
		p.members = append(p.members, &backendMember{addr: t})
	}
	return p
}

// order is the sequence to try: members that are answering, least loaded
// first, then the ones cooling down, soonest back first. A member cooling
// down is still tried rather than failing the connection outright.
func (p *backendPool) order() []*backendMember {
	now := p.now().UnixNano()
	n := len(p.members)
	start := int(p.turn.Add(1)-1) % max(n, 1)

	up := make([]*backendMember, 0, n)
	var cooling []*backendMember
	for i := range n {
		m := p.members[(start+i)%n]
		if m.downUntil.Load() > now {
			cooling = append(cooling, m)
		} else {
			up = append(up, m)
		}
	}
	// Stable, so the rotation above breaks ties between equal loads.
	sort.SliceStable(up, func(i, j int) bool { return up[i].active.Load() < up[j].active.Load() })
	sort.SliceStable(cooling, func(i, j int) bool {
		return cooling[i].downUntil.Load() < cooling[j].downUntil.Load()
	})
	return append(up, cooling...)
}

// dial connects to the best member and counts the connection against it.
// The caller must call done on the member it got once the connection ends.
func (p *backendPool) dial(ctx context.Context, network string) (net.Conn, *backendMember, error) {
	if len(p.members) == 0 {
		return nil, nil, errors.New("no backends configured")
	}
	order := p.order()
	var lastErr error
	for i, m := range order {
		timeout := memberDialTimeout
		if i == len(order)-1 {
			timeout = forwardDialTimeout
		}
		// Counted before the dial, not after it. Connections arrive in bursts
		// — a page, a speed test, sixteen streams at once — and a count taken
		// only once a dial had finished let every one of a burst see the same
		// loads and pile onto the same member: 16 streams over two kharej came
		// out at 1.8× one kharej instead of 2×, measured.
		m.active.Add(1)
		dialer := net.Dialer{Timeout: timeout}
		conn, err := dialer.DialContext(ctx, network, m.addr)
		if err == nil {
			m.downUntil.Store(0)
			return conn, m, nil
		}
		m.active.Add(-1)
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		m.downUntil.Store(p.now().Add(backendCooldown).UnixNano())
		lastErr = err
	}
	return nil, nil, lastErr
}

// done ends one connection's count against the member.
func (m *backendMember) done() { m.active.Add(-1) }
