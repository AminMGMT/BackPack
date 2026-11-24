package core

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/backpack/backpack/internal/metrics"
)

// What Backpack hangs on the ported engine from outside. Nothing here changes
// what crosses the wire or when; it is how the rest of Backpack — the panel,
// the quota, the health watchdog, the fallback chain and a reload — sees a
// tunnel that runs on this engine.

// Counted wraps the tunnel side of a relay so the tunnel's traffic counters
// see its bytes: a read arrived over the tunnel, a write left over it.
func Counted(c net.Conn) net.Conn { return metrics.CountedConn(c) }

func countIn(n int) {
	if n > 0 {
		metrics.AddBytes(uint64(n), 0)
	}
}

func countOut(n int) {
	if n > 0 {
		metrics.AddBytes(0, uint64(n))
	}
}

// Seated reports the peer that holds the control channel.
func Seated(addr string) { metrics.ReportPeer(addr) }

// Unseated forgets it.
func Unseated() { metrics.ClearPeer() }

// Status is a transport's status line, which Backhaul kept in an unguarded
// string written from several goroutines.
type Status struct{ v atomic.Value }

func (s *Status) Set(v string) { s.v.Store(v) }

func (s *Status) Get() string {
	v, _ := s.v.Load().(string)
	return v
}

// Running reports whether a control channel is up.
func (s *Status) Running() bool { return strings.HasPrefix(s.Get(), "Connected") }

// Holds counts the listeners a transport has open, so Wait can say when the
// ports are free — a reload starts the next tunnel as soon as Start returns.
type Holds struct {
	mu   sync.Mutex
	n    int
	free chan struct{}
}

// Hold records one more listener; the returned function lets it go.
func (h *Holds) Hold() func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.n == 0 {
		h.free = make(chan struct{})
	}
	h.n++
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.n--
			if h.n == 0 {
				close(h.free)
			}
		})
	}
}

// Wait blocks until no listener is held, or ctx ends.
func (h *Holds) Wait(ctx context.Context) {
	h.mu.Lock()
	if h.n == 0 {
		h.mu.Unlock()
		return
	}
	free := h.free
	h.mu.Unlock()
	select {
	case <-free:
	case <-ctx.Done():
	}
}
