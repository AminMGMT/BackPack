package transport

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// muxSession.failed, on the three transports that multiplex.
//
// It runs on a session goroutine that has just failed and is about to return,
// and it has three jobs: give back the session count, put the connection it was
// carrying back on the queue, and ask for a replacement. Getting any of them
// wrong is a leak that only shows up on a session that dies — which is rare
// enough that the end-to-end suite never reached this function on any of the
// three.
//
// The counters matter because the mux transports block on them. A session count
// that is never decremented is a session slot gone for the life of the run; a
// stream count that drifts the other way lets a session take more streams than
// MuxCon allows.

// sessionErrorSubject is the shape the three transports share here.
type sessionErrorSubject struct {
	name string
	// fail invokes muxSession.failed with a local connection, through the
	// transport's own adapter — so this also holds that the adapter binds the
	// right channels and the right counters.
	fail func(local *LocalTCPConn, err error)
	// counters reads the two atomics back.
	counters func() (session, stream int32)
	// setCounters primes them.
	setCounters func(session, stream int32)
	// queue is the channel the connection should land back on.
	queue chan LocalTCPConn
	// requests is the channel a replacement is asked for on.
	requests chan struct{}
	limits   *limiter
}

func muxSubjects(t *testing.T, queueSize int) []sessionErrorSubject {
	t.Helper()
	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)

	var subs []sessionErrorSubject

	{
		lim := newLimiter(Limits{MaxConnections: 8})
		g := &tcpMuxGen{
			localChannel:   make(chan LocalTCPConn, queueSize),
			reqNewConnChan: make(chan struct{}, 1),
		}
		s := &TcpMuxTransport{logger: log, limits: lim, config: &TcpMuxConfig{MuxCon: 8}}
		subs = append(subs, sessionErrorSubject{
			name: "tcpmux",
			fail: func(c *LocalTCPConn, err error) { s.session(g).failed(c, err) },
			counters: func() (int32, int32) {
				return atomic.LoadInt32(&s.sessionCounter), atomic.LoadInt32(&s.streamCounter)
			},
			setCounters: func(sc, st int32) {
				atomic.StoreInt32(&s.sessionCounter, sc)
				atomic.StoreInt32(&s.streamCounter, st)
			},
			queue: g.localChannel, requests: g.reqNewConnChan, limits: lim,
		})
	}
	{
		lim := newLimiter(Limits{MaxConnections: 8})
		g := &wsMuxGen{
			localChannel:   make(chan LocalTCPConn, queueSize),
			reqNewConnChan: make(chan struct{}, 1),
		}
		s := &WsMuxTransport{logger: log, limits: lim, config: &WsMuxConfig{MuxCon: 8}}
		subs = append(subs, sessionErrorSubject{
			name: "wsmux",
			fail: func(c *LocalTCPConn, err error) { s.session(g).failed(c, err) },
			counters: func() (int32, int32) {
				return atomic.LoadInt32(&s.sessionCounter), atomic.LoadInt32(&s.streamCounter)
			},
			setCounters: func(sc, st int32) {
				atomic.StoreInt32(&s.sessionCounter, sc)
				atomic.StoreInt32(&s.streamCounter, st)
			},
			queue: g.localChannel, requests: g.reqNewConnChan, limits: lim,
		})
	}
	{
		lim := newLimiter(Limits{MaxConnections: 8})
		g := &kcpGen{
			localChannel:   make(chan LocalTCPConn, queueSize),
			reqNewConnChan: make(chan struct{}, 1),
		}
		s := &KcpTransport{logger: log, limits: lim, config: &KcpConfig{MuxCon: 8}}
		subs = append(subs, sessionErrorSubject{
			name: "kcp",
			fail: func(c *LocalTCPConn, err error) { s.session(g).failed(c, err) },
			counters: func() (int32, int32) {
				return atomic.LoadInt32(&s.sessionCounter), atomic.LoadInt32(&s.streamCounter)
			},
			setCounters: func(sc, st int32) {
				atomic.StoreInt32(&s.sessionCounter, sc)
				atomic.StoreInt32(&s.streamCounter, st)
			},
			queue: g.localChannel, requests: g.reqNewConnChan, limits: lim,
		})
	}
	return subs
}

// A session that dies gives its slot back and puts its passenger on the queue.
func TestAFailedSessionRequeuesItsConnection(t *testing.T) {
	for _, s := range muxSubjects(t, 4) {
		t.Run(s.name, func(t *testing.T) {
			s.setCounters(3, 5)
			if !s.limits.acquire() {
				t.Fatal("setup: no slot")
			}

			local := LocalTCPConn{conn: &pairedConn{}, remoteAddr: "127.0.0.1:80", timeCreated: nowMillis()}
			s.fail(&local, errors.New("the session went away"))

			session, stream := s.counters()
			if session != 2 {
				t.Errorf("session counter = %d, want 2 — a session that died kept its slot", session)
			}
			// The connection went back on the queue, so it is still in flight
			// and still counted.
			if stream != 5 {
				t.Errorf("stream counter = %d, want it untouched for a re-queued connection", stream)
			}
			select {
			case got := <-s.queue:
				if got.remoteAddr != "127.0.0.1:80" {
					t.Errorf("the wrong connection was re-queued: %+v", got)
				}
			default:
				t.Error("the connection was not put back on the queue, so the client it " +
					"belongs to waits for a stream that will never be opened")
			}
			// It is still in flight, so its slot is still held.
			if s.limits.active.Load() != 1 {
				t.Errorf("the slot was released for a connection that is still in flight (held: %d)",
					s.limits.active.Load())
			}
			// And a replacement session was asked for.
			select {
			case <-s.requests:
			default:
				t.Error("no replacement was requested, so a pool that lost its last " +
					"session never refills")
			}
		})
	}
}

// When there is no room to re-queue, the connection is counted out — because
// nothing downstream will ever do it.
func TestAConnectionThatCannotBeRequeuedIsCountedOut(t *testing.T) {
	for _, s := range muxSubjects(t, 1) {
		t.Run(s.name, func(t *testing.T) {
			// Fill the queue so the re-queue cannot succeed.
			s.queue <- LocalTCPConn{conn: &pairedConn{}, timeCreated: nowMillis()}
			s.setCounters(1, 4)
			if !s.limits.acquire() {
				t.Fatal("setup: no slot")
			}

			dropped := &pairedConn{}
			local := LocalTCPConn{conn: dropped, remoteAddr: "127.0.0.1:80", timeCreated: nowMillis()}
			s.fail(&local, errors.New("the session went away"))

			session, stream := s.counters()
			if session != 0 {
				t.Errorf("session counter = %d, want 0", session)
			}
			if stream != 3 {
				t.Errorf("stream counter = %d, want 3 — a connection nothing downstream "+
					"will handle has to be counted out here", stream)
			}
			if !dropped.wasClosed() {
				t.Error("the connection that could not be re-queued was left open")
			}
			if s.limits.active.Load() != 0 {
				t.Errorf("the slot of a dropped connection was not returned (held: %d)",
					s.limits.active.Load())
			}
		})
	}
}

// A full request channel must not block the session goroutine that is trying to
// return. It runs on the goroutine that has just failed, so there may be
// nothing left to drain either channel.
func TestAFullRequestChannelDoesNotBlockTheFailingSession(t *testing.T) {
	for _, s := range muxSubjects(t, 4) {
		t.Run(s.name, func(t *testing.T) {
			s.requests <- struct{}{} // fill it
			s.setCounters(1, 1)
			if !s.limits.acquire() {
				t.Fatal("setup: no slot")
			}

			done := make(chan struct{})
			go func() {
				defer close(done)
				local := LocalTCPConn{conn: &pairedConn{}, timeCreated: nowMillis()}
				s.fail(&local, errors.New("boom"))
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("the session-failure path blocked on a full request channel, so the " +
					"session goroutine never returns and its slot never comes back")
			}
		})
	}
}

// requeueLocal is the piece all three share, and its contract is that a
// connection it cannot place is closed and its slot returned — never silently
// dropped while still counted.
func TestRequeueLocalNeverLosesASlot(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)

	t.Run("there is room", func(t *testing.T) {
		lim := newLimiter(Limits{MaxConnections: 4})
		lim.acquire()
		ch := make(chan LocalTCPConn, 1)
		conn := &pairedConn{}
		if !requeueLocal(ch, LocalTCPConn{conn: conn}, lim, log) {
			t.Fatal("a re-queue with room reported failure")
		}
		if conn.wasClosed() {
			t.Error("a re-queued connection was closed")
		}
		if lim.active.Load() != 1 {
			t.Error("a re-queued connection lost its slot")
		}
	})

	t.Run("there is no room", func(t *testing.T) {
		lim := newLimiter(Limits{MaxConnections: 4})
		lim.acquire()
		ch := make(chan LocalTCPConn) // unbuffered, nothing reading
		conn := &pairedConn{}
		if requeueLocal(ch, LocalTCPConn{conn: conn}, lim, log) {
			t.Fatal("a re-queue with no room reported success")
		}
		if !conn.wasClosed() {
			t.Error("a connection that could not be re-queued was left open")
		}
		if lim.active.Load() != 0 {
			t.Error("a dropped connection kept its slot, which is the leak this guards")
		}
	})

	// A connection with no socket behind it must not panic on the drop path.
	t.Run("nothing to close", func(t *testing.T) {
		lim := newLimiter(Limits{MaxConnections: 4})
		lim.acquire()
		ch := make(chan LocalTCPConn)
		if requeueLocal(ch, LocalTCPConn{}, lim, log) {
			t.Fatal("reported success with nowhere to put it")
		}
		if lim.active.Load() != 0 {
			t.Error("the slot was not returned")
		}
	})
}

var _ = net.IPv4zero
