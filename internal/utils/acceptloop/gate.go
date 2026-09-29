package acceptloop

import (
	"net"
	"sync"
	"time"
)

// Gate bounds the connections a listener holds before they have proved the
// tunnel token.
//
// Everything a server does before a peer has proved anything is done for
// anyone who can reach its port. Each such connection is a goroutine waiting
// on a deadline — the announcement's, the Noise handshake's, the control
// claim's — and without a bound, whoever opens connections fastest decides how
// many there are. A host may hold PerHost of them at once and the listener
// Total; past that a new one is closed at once.
//
// A bound on strangers must not become a way to shut out the genuine client,
// and a Total that anyone can fill would be exactly that. So a host that has
// proved the token (Prove) passes without taking a place, for as long as it
// keeps proving it: its control re-dials and its pool refills are never
// refused, however full the gate is. A stranger cannot pass as one — a TCP
// source address cannot be forged into a completed connection — and IPv6 hosts
// are counted by /64, the block one subscriber is given, so a single one cannot
// multiply itself into the whole Total.
//
// The zero value uses the defaults.
type Gate struct {
	// PerHost and Total are the bounds; zero uses DefaultPerHost and
	// DefaultTotal.
	PerHost int
	Total   int

	mu     sync.Mutex
	byHost map[string]int
	all    int
	proven map[string]time.Time
}

// The default bounds: far above what a genuine client holds unproven at once,
// far below what would matter to the machine.
const (
	DefaultPerHost = 128
	DefaultTotal   = 1024
)

// provenFor is how long a host that proved the token passes without a place.
// Renewed every time it proves it again, which a working client does with every
// pool connection.
const provenFor = time.Hour

// maxProven bounds the remembered hosts; the oldest is forgotten first.
const maxProven = 64

// hostKey is what a connection is counted under: its IPv4 address, or its
// IPv6 /64.
func hostKey(addr net.Addr) string {
	host := addr.String()
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// Prove records that a connection from addr has proved the token.
func (g *Gate) Prove(addr net.Addr) {
	key := hostKey(addr)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.proven == nil {
		g.proven = map[string]time.Time{}
	}
	g.proven[key] = time.Now()
	if len(g.proven) > maxProven {
		oldest, at := "", time.Time{}
		for k, t := range g.proven {
			if oldest == "" || t.Before(at) {
				oldest, at = k, t
			}
		}
		delete(g.proven, oldest)
	}
}

// Enter reserves a place for one connection from addr; leave gives it back once
// the connection has been judged. ok is false when there is no place. A host
// that has proved the token needs none: leave is then a no-op.
func (g *Gate) Enter(addr net.Addr) (leave func(), ok bool) {
	key := hostKey(addr)
	g.mu.Lock()
	defer g.mu.Unlock()
	if at, ok := g.proven[key]; ok && time.Since(at) < provenFor {
		return func() {}, true
	}
	perHost, total := g.PerHost, g.Total
	if perHost == 0 {
		perHost = DefaultPerHost
	}
	if total == 0 {
		total = DefaultTotal
	}
	if g.all >= total || g.byHost[key] >= perHost {
		return nil, false
	}
	if g.byHost == nil {
		g.byHost = map[string]int{}
	}
	g.byHost[key]++
	g.all++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.all--
			if g.byHost[key]--; g.byHost[key] <= 0 {
				delete(g.byHost, key)
			}
		})
	}, true
}

// Admit is Enter for an accepted connection: when there is no place the
// connection is closed at once, and logf is told why.
func (g *Gate) Admit(conn net.Conn, logf func(string, ...any)) (leave func(), ok bool) {
	leave, ok = g.Enter(conn.RemoteAddr())
	if !ok {
		logf("refusing a connection from %s: too many connections that have not proved the token", conn.RemoteAddr())
		conn.Close()
	}
	return leave, ok
}
