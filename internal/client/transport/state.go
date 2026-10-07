package transport

import (
	"context"
	"io"
	"net"
	"sync"

	"github.com/backpack/backpack/internal/web"
	"github.com/gorilla/websocket"
)

// A client transport rebuilds its state on every reconnect: Restart() swaps the
// context, the control channel, the usage monitor and the counters while the
// previous generation's goroutines are still winding down. Those goroutines are
// reading the very fields being replaced, which is a data race — and the values
// involved are pointers and channels, where an unsynchronised read is not
// merely stale but undefined.
//
// clientState puts that generation-scoped state behind one lock so a reader
// always sees a complete, consistent generation.

// clientState holds everything Restart() replaces.
type clientState struct {
	mu           sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
	conn         net.Conn        // control channel for the byte-stream transports
	wsConn       *websocket.Conn // control channel for the websocket transports
	usageMonitor *web.Usage
	workers      sync.WaitGroup
	stopping     bool
}

// Reset publishes a whole new generation at once, so no reader can observe a
// half-swapped mixture of old and new.
func (s *clientState) Reset(ctx context.Context, cancel context.CancelFunc, usage *web.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx = ctx
	s.cancel = cancel
	s.usageMonitor = usage
	s.conn = nil
	s.wsConn = nil
	s.stopping = false
}

// Go registers work before starting it. Restart seals the generation before
// waiting, so a late pool request cannot attach work to its replacement.
func (s *clientState) Go(work func()) {
	s.mu.Lock()
	if s.stopping || s.ctx == nil || s.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	s.workers.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.workers.Done()
		work()
	}()
}

func (s *clientState) Stop() {
	s.mu.Lock()
	s.stopping = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.CloseConn()
}

func (s *clientState) Wait() { s.workers.Wait() }

// Own interrupts even a connection waiting for its first destination. The
// caller must defer the returned cleanup for errors and normal completion.
func (s *clientState) Own(conn io.Closer) func() {
	stop := context.AfterFunc(s.Ctx(), func() { _ = conn.Close() })
	return func() {
		stop()
		_ = conn.Close()
	}
}

func (s *clientState) Ctx() context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ctx
}

func (s *clientState) Cancel() context.CancelFunc {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cancel
}

func (s *clientState) Usage() *web.Usage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.usageMonitor
}

func (s *clientState) Conn() net.Conn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.conn
}

func (s *clientState) SetConn(c net.Conn) bool {
	s.mu.Lock()
	if s.stopping || (s.ctx != nil && s.ctx.Err() != nil) {
		s.mu.Unlock()
		if c != nil {
			_ = c.Close()
		}
		return false
	}
	s.conn = c
	s.mu.Unlock()
	return true
}

func (s *clientState) WSConn() *websocket.Conn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.wsConn
}

func (s *clientState) SetWSConn(c *websocket.Conn) bool {
	s.mu.Lock()
	if s.stopping || (s.ctx != nil && s.ctx.Err() != nil) {
		s.mu.Unlock()
		if c != nil {
			_ = c.Close()
		}
		return false
	}
	s.wsConn = c
	s.mu.Unlock()
	return true
}

// CloseConn closes whichever control channel is held, if any.
func (s *clientState) CloseConn() {
	if c := s.Conn(); c != nil {
		c.Close()
	}
	if c := s.WSConn(); c != nil {
		c.Close()
	}
}

// drain empties a buffered signal channel without replacing it. Restart used to
// allocate a new channel, which races with the goroutines selecting on the old
// one; emptying the existing channel achieves the same thing safely.
func drain(ch chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// tunnelStatus is the one-line state a transport publishes for the panel.
// Written as a run starts and again when its control channel comes up, and
// cleared by Restart — three writers across two generations that overlap, on
// what was a plain string field. clientState above exists for exactly this
// reason; the status was simply left out of it.
type tunnelStatus struct {
	mu sync.RWMutex
	s  string
}

func (t *tunnelStatus) set(v string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.s = v
}

func (t *tunnelStatus) get() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.s
}
