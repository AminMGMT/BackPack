package direct

import (
	"sync"

	"github.com/backpack/backpack/internal/metrics"
)

// sessionSet holds the authenticated sessions and publishes their state under
// the same lock. Losing one of several sessions must not report a disconnect.
type sessionSet struct {
	mu   sync.RWMutex
	list []*tunnelSession
}

func (s *sessionSet) add(session *tunnelSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(append([]*tunnelSession(nil), s.list...), session)
	s.reportPeer()
}

func (s *sessionSet) remove(session *tunnelSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make([]*tunnelSession, 0, len(s.list))
	for _, candidate := range s.list {
		if candidate != session {
			next = append(next, candidate)
		}
	}
	s.list = next
	s.reportPeer()
}

func (s *sessionSet) snapshot() []*tunnelSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.list
}

// reportPeer is called with mu held, so an old session's departure cannot
// overwrite a replacement's arrival. Readers get an immutable session list.
func (s *sessionSet) reportPeer() {
	for _, session := range s.list {
		if session.Usable() {
			metrics.ReportPeer(session.RemoteAddr().String())
			return
		}
	}
	metrics.ClearPeer()
}
