package transport

import (
	"context"
	"sync"
	"time"
)

// Knowing when a transport has actually let go of its ports.
//
// Start returned when the transport's supervisor stopped, not when the
// goroutines it launched had. So for a window afterwards the old run still held
// its listeners — and a reload builds the next generation as soon as the
// previous Start returns, which means the two fight for the same ports.
//
// Production papered over it with a two-second sleep in Restart, and the
// comment there says as much. A sleep is a guess: usually long enough, never a
// guarantee, and silently wrong on a loaded machine. It also made an end-to-end
// test flake, which is how this was found.
//
// What is tracked is deliberately narrow: the goroutines that *hold a
// listener*, not every goroutine a transport starts. Accept loops, handle
// loops, pool maintainers and heartbeats all end on their own and none of them
// holds a port. The question being answered is "can the next generation bind",
// and only a listener can answer it wrongly.

// listenerSet counts the listeners a transport currently holds.
//
// It is on the transport rather than on the generation on purpose: at shutdown
// the answer wanted is "have *all* of them gone", including any belonging to a
// generation that a restart replaced while this one was winding down.
//
// # Why not a WaitGroup
//
// A WaitGroup is the obvious choice and it is wrong here, which the race
// detector said immediately. Its contract is that every Add happens before
// Wait — and these Adds happen *inside* the listener goroutines, which the
// transport launched and did not wait for. A listener that binds a moment after
// shutdown begins is an Add racing a Wait, which is a data race however the
// counter happens to land.
//
// A counter and a channel have no such ordering requirement: hold and release
// take the lock, and wait takes a snapshot of the channel under the same lock.
// A listener that arrives late simply reopens the gate.
type listenerSet struct {
	mu sync.Mutex
	n  int
	// zero is closed while n is 0 and open while it is not, so wait can block
	// on it without polling. nil means "never held anything", which is the same
	// answer as zero for every caller.
	zero chan struct{}
}

// hold is called by a goroutine that has just bound a listener; release when it
// has closed it. Both sides in the same function, so a listener cannot be
// counted without being uncounted.
func (l *listenerSet) hold() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n == 0 {
		l.zero = make(chan struct{})
	}
	l.n++
}

func (l *listenerSet) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.n--
	if l.n <= 0 {
		l.n = 0
		if l.zero != nil {
			close(l.zero)
			l.zero = nil
		}
	}
}

// listenerShutdown is how long wait gives the listeners before giving up.
//
// Generous, because the alternative to waiting is the race this exists to
// remove. Bounded, because a listener that will not close is a bug and holding
// the process open for it turns that bug into a hang — a service that will not
// stop is worse than one that stops untidily, since systemd escalates to
// SIGKILL and the operator learns nothing either way.
const listenerShutdown = 10 * time.Second

// wait blocks until every listener has closed, the context ends, or the
// deadline passes.
func (l *listenerSet) wait(ctx context.Context) {
	l.mu.Lock()
	gate := l.zero
	l.mu.Unlock()
	if gate == nil {
		return // nothing is held
	}

	t := time.NewTimer(listenerShutdown)
	defer t.Stop()
	select {
	case <-gate:
	case <-t.C:
	case <-ctx.Done():
		// The caller is going away regardless; waiting longer helps nobody.
	}
}

// Wait blocks until this transport has closed every listener it holds.
//
// One method per transport, for the same reason Running is: the set is held by
// value on each struct, and embedding it to share a method would change the
// memory layout of all seven for no gain.
func (s *TcpTransport) Wait(ctx context.Context)    { s.listeners.wait(ctx) }
func (s *TcpMuxTransport) Wait(ctx context.Context) { s.listeners.wait(ctx) }
func (s *WsTransport) Wait(ctx context.Context)     { s.listeners.wait(ctx) }
func (s *WsMuxTransport) Wait(ctx context.Context)  { s.listeners.wait(ctx) }
func (s *KcpTransport) Wait(ctx context.Context)    { s.listeners.wait(ctx) }
func (s *QuicTransport) Wait(ctx context.Context)   { s.listeners.wait(ctx) }
func (s *UdpTransport) Wait(ctx context.Context)    { s.listeners.wait(ctx) }
