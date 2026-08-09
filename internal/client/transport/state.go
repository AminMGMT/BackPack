package transport

import (
	"context"
	"io"
	"net"
	"sync"

	"github.com/backpack/backpack/internal/web"
	"github.com/gorilla/websocket"
)

// clientState owns one complete transport generation. Restart stops and waits
// for that generation before Reset publishes the next one, so an old worker can
// never accidentally start reading the new generation's context or control
// connection.
type clientState struct {
	mu           sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
	conn         net.Conn
	wsConn       *websocket.Conn
	usageMonitor *web.Usage

	workers  sync.WaitGroup
	stopping bool
	closers  map[uint64]io.Closer
	nextID   uint64
}

// Reset publishes a new generation. The caller must StopAndWait first whenever
// a previous generation has been started.
func (s *clientState) Reset(ctx context.Context, cancel context.CancelFunc, usage *web.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx = ctx
	s.cancel = cancel
	s.usageMonitor = usage
	s.conn = nil
	s.wsConn = nil
	s.stopping = false
	s.closers = make(map[uint64]io.Closer)
	s.nextID = 0
}

func (s *clientState) Ctx() context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ctx
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
	if s.stopping {
		s.mu.Unlock()
		c.Close()
		return false
	}
	s.conn = c
	s.addCloserLocked(c)
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
	if s.stopping {
		s.mu.Unlock()
		c.Close()
		return false
	}
	s.wsConn = c
	s.addCloserLocked(c)
	s.mu.Unlock()
	return true
}

// Go starts a worker only while the generation is live. Add and Stop share the
// same lock, which makes Wait safe: once Stop returns, no worker can be added.
func (s *clientState) Go(fn func()) bool {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return false
	}
	s.workers.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.workers.Done()
		fn()
	}()
	return true
}

// Track registers a blocking connection or session for forced closure at
// generation shutdown. The returned function removes resources which finish
// normally. A connection that arrives after Stop is closed immediately.
func (s *clientState) Track(c io.Closer) (untrack func(), ok bool) {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		c.Close()
		return func() {}, false
	}
	id := s.addCloserLocked(c)
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.closers, id)
			s.mu.Unlock()
		})
	}, true
}

func (s *clientState) addCloserLocked(c io.Closer) uint64 {
	s.nextID++
	s.closers[s.nextID] = c
	return s.nextID
}

// Stop cancels the generation and closes every registered blocking resource.
// Close calls happen outside the state lock because third-party implementations
// are allowed to run callbacks while closing.
func (s *clientState) Stop() {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return
	}
	s.stopping = true
	cancel := s.cancel
	closers := make([]io.Closer, 0, len(s.closers))
	for _, c := range s.closers {
		closers = append(closers, c)
	}
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	for _, c := range closers {
		_ = c.Close()
	}
}

func (s *clientState) Wait() {
	s.workers.Wait()
}

func (s *clientState) StopAndWait() {
	s.Stop()
	s.Wait()
}

// drain empties a buffered signal channel without replacing it. Replacing the
// channel would strand workers which already selected on the old one.
func drain(ch chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
