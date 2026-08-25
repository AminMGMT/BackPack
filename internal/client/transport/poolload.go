package transport

import (
	"sync/atomic"
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

	// smuxPoolMemoryBudget preserves the maximum receive-window footprint of
	// the historical default (8 sessions * 4 growth * 4 MiB). Larger receive
	// windows therefore trade automatic pool growth for a predictable ceiling
	// instead of silently multiplying the process's memory exposure.
	smuxPoolMemoryBudget = 128 * 1024 * 1024
)

// sessionSlots prevents concurrent pool and control-channel requests from
// racing past a transport's calculated session limit.
type sessionSlots struct {
	limit int32
	used  atomic.Int32
}

func newSessionSlots(limit int) *sessionSlots {
	return &sessionSlots{limit: int32(limit)}
}

func (s *sessionSlots) tryAcquire() bool {
	for {
		used := s.used.Load()
		if used >= s.limit {
			return false
		}
		if s.used.CompareAndSwap(used, used+1) {
			return true
		}
	}
}

func (s *sessionSlots) release() {
	s.used.Add(-1)
}

func (s *sessionSlots) max() int {
	return int(s.limit)
}

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
	return p.wantsMoreWithin(mbps, liveConns, poolSize, configuredSize, configuredSize*poolGrowthLimit)
}

func (p *poolLoad) wantsMoreWithin(mbps, liveConns, poolSize, configuredSize, maxSize int) bool {
	if mbps <= 0 || liveConns <= 0 {
		return false
	}
	if !poolCanGrowWithin(poolSize, configuredSize, maxSize) {
		return false
	}
	return mbps/liveConns >= poolScaleMbpsPerConn
}

// poolCanGrow bounds the pool so neither trigger can grow it without limit.
func poolCanGrow(poolSize, configuredSize int) bool {
	return poolCanGrowWithin(poolSize, configuredSize, configuredSize*poolGrowthLimit)
}

func poolCanGrowWithin(poolSize, configuredSize, maxSize int) bool {
	if configuredSize <= 0 || maxSize < configuredSize {
		return false
	}
	return poolSize < maxSize
}

// muxPoolLimit applies the normal 4x growth cap while also bounding the
// aggregate SMUX receive windows. An explicitly configured initial pool is
// always honoured, even when it exceeds the automatic-growth budget.
func muxPoolLimit(configuredSize, maxReceiveBuffer int) int {
	if configuredSize <= 0 {
		return 0
	}

	growthLimit := configuredSize * poolGrowthLimit
	if maxReceiveBuffer <= 0 {
		return growthLimit
	}

	memoryLimit := smuxPoolMemoryBudget / maxReceiveBuffer
	if memoryLimit < configuredSize {
		return configuredSize
	}
	if memoryLimit < growthLimit {
		return memoryLimit
	}
	return growthLimit
}
