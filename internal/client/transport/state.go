package transport

import (
	"context"
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

func (s *clientState) SetConn(c net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn = c
}

func (s *clientState) WSConn() *websocket.Conn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.wsConn
}

func (s *clientState) SetWSConn(c *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wsConn = c
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
