package transport

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/controlwire"
	"github.com/backpack/backpack/internal/utils"
	"github.com/sirupsen/logrus"
)

// controlLoop serves one generation's control channel on the client side: it
// dials a pool connection whenever the server asks for one, answers the
// server's probes, notices a server that has gone quiet, and says goodbye —
// and closes the channel — when the generation ends.
//
// All seven transports had their own copy, and the copies had drifted: only
// the KCP one had been taught not to blame the path for a timeout that the
// beat clock had already explained (#45), and every copy read and wrote
// through whatever control channel the transport held at that moment rather
// than the one its generation was started with. It is here once now, bound to
// its generation's context and channel; a transport chooses only how the
// signals travel (link, see controlwire) and whether its server expects each
// heartbeat answered.
type controlLoop struct {
	ctx  context.Context
	link controlwire.Link
	// keepAlive is how long the channel may stay silent before the server is
	// presumed gone, until the beat clock has learned the server's rhythm.
	// See beatClock.
	keepAlive time.Duration
	log       *logrus.Logger
	// restart ends this generation and starts the next; called in its own
	// goroutine, since it waits for this one to finish.
	restart func()
	// asked answers the server's request for one more pool connection; see
	// lifecycle.serverAsked.
	asked func()
	// echoBeats answers every heartbeat: the websocket servers listen for it.
	echoBeats bool
}

// control builds the loop for the generation that is starting now.
func (l *lifecycle) control(link controlwire.Link, keepAlive time.Duration, dial, restart func()) controlLoop {
	ctx := l.state.Ctx()
	return controlLoop{
		ctx:       ctx,
		link:      link,
		keepAlive: keepAlive,
		log:       l.logger,
		restart: func() {
			// The goroutine requesting recovery may be scheduled only after
			// its generation has already been replaced.
			if ctx.Err() == nil {
				restart()
			}
		},
		asked: func() { l.serverAsked(dial) },
	}
}

// run serves the channel until the generation ends or the channel fails; a
// failure asks for a restart.
func (c controlLoop) run() {
	// Let a healthy channel carry the goodbye, but interrupt a stalled write
	// promptly instead of leaving shutdown behind the full write timeout.
	stop := context.AfterFunc(c.ctx, func() { time.AfterFunc(100*time.Millisecond, c.link.Close) })
	defer stop()
	defer c.link.Close()
	done := make(chan struct{})
	defer close(done)

	signals := make(chan byte, 16)
	go c.read(signals, done)

	for {
		select {
		case <-c.ctx.Done():
			// The goodbye, then the channel itself: it belongs to the
			// generation that just ended. Left open, it outlived its
			// generation whenever the tunnel was ended without a restart — a
			// reload, a fallback chain moving on — and over a datagram
			// carrier nothing else ever closes it.
			_ = c.link.Send(utils.SG_Closed)
			c.link.Close()
			return

		case signal := <-signals:
			switch signal {
			case utils.SG_Chan:
				c.asked()

			case utils.SG_HB:
				c.log.Debug("heartbeat signal received successfully")
				if c.echoBeats {
					if err := c.link.Send(utils.SG_HB); err != nil {
						c.log.Errorf("failed to answer the heartbeat: %v", err)
						go c.restart()
						return
					}
				}

			case utils.SG_RTT:
				if err := c.link.Send(utils.SG_RTT); err != nil {
					c.log.Errorf("failed to answer the RTT probe: %v", err)
					go c.restart()
					return
				}

			case utils.SG_Closed:
				c.log.Warn("control channel has been closed by the server")
				go c.restart()
				return

			default:
				c.log.Errorf("unexpected signal on the control channel: %v", signal)
				go c.restart()
				return
			}
		}
	}
}

// read hands every signal the server sends to the loop, bounding each read by
// what the beat clock expects, until the channel fails or the loop returns.
//
// A datagram carrier gives no signal when the peer disappears — there is no
// connection for the operating system to tear down — so without the bound a
// read on a dead tunnel would block forever and the client would never
// reconnect. Over TCP the kernel ends the read eventually, eleven minutes later
// on the shipped keepalive.
func (c controlLoop) read(signals chan<- byte, done <-chan struct{}) {
	beats := newBeatClock(time.Now())
	for {
		if err := c.link.SetReadDeadline(time.Now().Add(beats.deadline(c.keepAlive))); err != nil {
			if c.live(done) {
				c.log.Errorf("failed to set control channel deadline: %v", err)
				go c.restart()
			}
			return
		}
		signal, err := c.link.Receive()
		if errors.Is(err, controlwire.ErrNoSignal) {
			c.log.Warnf("ignoring a malformed control frame: %v", err)
			continue
		}
		if err != nil {
			if c.live(done) {
				// A timeout is explained by the beat clock, which can tell a
				// server that stopped heartbeating from one whose heartbeat
				// never reached this client in time. A line that blamed the
				// path for it sent people looking at the wrong thing (#45).
				if hint := beats.explain(err, c.keepAlive); hint != "" {
					c.log.Warn(hint)
				} else {
					c.log.Errorf("failed to read from control channel: %v", err)
				}
				go c.restart()
			}
			return
		}
		if signal == utils.SG_HB {
			beats.beat(time.Now())
		}
		select {
		case signals <- signal:
		case <-done:
			return
		}
	}
}

// live reports whether a failure is still this generation's to act on: one
// that has ended, or whose loop has already asked for the restart, must not
// queue another restart of a tunnel on its way down.
func (c controlLoop) live(done <-chan struct{}) bool {
	select {
	case <-done:
		return false
	default:
		return c.ctx.Err() == nil
	}
}

// serverAsked accounts for one request for a pool connection and, unless one
// is already on its way for it, dials.
func (l *lifecycle) serverAsked(dial func()) {
	atomic.AddInt32(&l.loadConnections, 1)
	select {
	case <-l.controlFlow:
		// The pool has asked to retire a connection: leaving this request
		// unanswered is how it shrinks. See poolMaintainer.
	default:
		l.logger.Debug("channel signal received, initiating tunnel dialer")
		l.state.Go(dial)
	}
}
