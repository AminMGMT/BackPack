package transport

import (
	"time"

	"github.com/backpack/backpack/internal/metrics"
)

// Scaling the connection pool on throughput, not only on churn.
//
// The pool grows when the server asks for new connections faster than the pool
// supplies them. That is a good signal for many short connections and blind to
// the opposite case: a few long-lived streams — one large download, a video
// call, a backup — ask for a connection once and then saturate it for minutes.
// The request counter sits near zero the whole time, so the pool never grows
// however full the pipe gets, and the tunnel stays limited to the connections
// it happened to open at the start.
//
// Throughput is the missing signal, and it is already being measured: the
// metrics collector counts every byte the tunnel carries, so reading it costs
// two atomic loads per tick.
//
// This only adds a reason to grow. The existing trigger is untouched, and the
// transports that do not count bytes (ws, udp) simply never see this fire and
// behave exactly as before.

const (
	// poolScaleMbpsPerConn is the sustained throughput, per live physical
	// connection, above which another connection is likely to help.
	//
	// It is a heuristic, not a derivation: a connection holding this much for a
	// full interval is doing real work rather than idling, and at that point a
	// second flow generally beats a bigger window on one. With the default pool
	// of 8 this starts growing at roughly 100 Mbit/s, which is about where a
	// single TCP flow on a long path stops being able to fill the link on its
	// own.
	poolScaleMbpsPerConn = 12

	// poolGrowthLimit caps the pool at this multiple of the configured size.
	// Both triggers respect it: growth was previously unbounded, so a burst of
	// requests could keep adding connections with nothing to stop it.
	poolGrowthLimit = 4
)

// poolLoad turns the tunnel's cumulative byte counters into a per-interval
// throughput reading.
type poolLoad struct {
	lastIn, lastOut uint64
	lastAt          time.Time
}

// mbps reports the throughput carried since the previous call, in Mbit/s.
//
// The first call has no previous reading to subtract and returns 0, as does a
// call where the counters went backwards — which happens when the metrics file
// is restored from a backup. Both mean "no opinion", not "idle".
func (p *poolLoad) mbps() int {
	in, out := metrics.Traffic()
	now := time.Now()

	prevIn, prevOut, prevAt := p.lastIn, p.lastOut, p.lastAt
	p.lastIn, p.lastOut, p.lastAt = in, out, now

	if prevAt.IsZero() || in < prevIn || out < prevOut {
		return 0
	}
	secs := now.Sub(prevAt).Seconds()
	if secs <= 0 {
		return 0
	}
	bits := float64((in-prevIn)+(out-prevOut)) * 8
	return int(bits / secs / 1e6)
}

// wantsMore reports whether measured throughput justifies another connection.
//
// liveConns is how many physical connections the pool actually has right now;
// dividing by it is what makes this a statement about how hard each connection
// is working rather than about the tunnel's total speed.
func (p *poolLoad) wantsMore(mbps, liveConns, poolSize, configuredSize int) bool {
	if mbps <= 0 || liveConns <= 0 {
		return false
	}
	if !poolCanGrow(poolSize, configuredSize) {
		return false
	}
	return mbps/liveConns >= poolScaleMbpsPerConn
}

// poolCanGrow bounds the pool so neither trigger can grow it without limit.
func poolCanGrow(poolSize, configuredSize int) bool {
	if configuredSize <= 0 {
		return false
	}
	return poolSize < configuredSize*poolGrowthLimit
}
