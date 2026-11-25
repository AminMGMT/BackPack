package server

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/config"
)

// A tunnel whose two servers are updated one at a time.
//
// tcp, tcpmux, ws, wss, wsmux and wssmux run on the plain engine,
// whose handshake Backpack v2.0.0 does not speak, nor this engine v2.0.0's. So
// an Iran server updated before its kharej would lose the tunnel until the
// kharej was updated too. Instead it serves each kharej on the engine that
// kharej speaks: it starts on the engine the tunnel last ran, moves to the
// v2.0.0 engine when a v2.0.0 kharej claims the tunnel, and back to the plain
// engine when an updated kharej does. Each move is a restart of a few seconds,
// once per update; the engine in use is remembered beside the config, so a
// restart does not try the wrong one first.

const (
	enginePlain = "plain"
	engineV2    = "v2"
)

// compatPinPath is where the engine in use is remembered: beside the peer
// pin, <dir>/.<name>.compat. Empty when there is nowhere to keep it.
func compatPinPath(peerPin string) string {
	if peerPin == "" {
		return ""
	}
	return strings.TrimSuffix(peerPin, ".peer") + ".compat"
}

// compatRunner is the engine serving the tunnel now, swapped as the kharej
// asks.
type compatRunner struct {
	mu   sync.Mutex
	cur  runner
	want chan string
	done chan struct{}
}

func (c *compatRunner) Running() bool {
	c.mu.Lock()
	r := c.cur
	c.mu.Unlock()
	return r != nil && r.Running()
}

// Wait blocks until the tunnel has stopped and let go of its ports.
func (c *compatRunner) Wait(ctx context.Context) {
	select {
	case <-c.done:
	case <-ctx.Done():
	}
}

// ask requests a move to engine; a request already pending wins.
func (c *compatRunner) ask(engine string) {
	select {
	case c.want <- engine:
	default:
	}
}

func (s *Server) startCompat(ctx context.Context, tr config.TransportType) runner {
	// A candidate whose run has already ended starts nothing.
	if ctx.Err() != nil {
		return nil
	}
	pin := compatPinPath(s.config.PeerPinPath)
	mode := enginePlain
	if b, err := os.ReadFile(pin); pin != "" && err == nil && strings.TrimSpace(string(b)) == engineV2 {
		mode = engineV2
	}
	c := &compatRunner{want: make(chan string, 1), done: make(chan struct{})}
	start := func(engine string, run context.Context) runner {
		if engine == engineV2 {
			return s.startEngine(run, tr, func() { c.ask(enginePlain) })
		}
		return s.startPlain(run, tr, func() { c.ask(engineV2) })
	}

	go func() {
		defer close(c.done)
		for {
			run, stop := context.WithCancel(ctx)
			r := start(mode, run)
			c.mu.Lock()
			c.cur = r
			c.mu.Unlock()

			var next string
			for next == "" {
				select {
				case <-ctx.Done():
				case want := <-c.want:
					if want == mode {
						continue
					}
					next = want
				}
				if ctx.Err() != nil {
					break
				}
			}
			stop()
			// The next engine binds the same ports: wait for these to be free.
			wait, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			r.Wait(wait)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if next == engineV2 {
				s.logger.Infof("tunnel on %s: serving a Backpack v2.0.0 kharej on its engine until it is updated", tr)
			} else {
				s.logger.Infof("tunnel on %s: the kharej is updated — back on the plain engine", tr)
			}
			mode = next
			if pin != "" {
				if mode == engineV2 {
					_ = os.WriteFile(pin, []byte(engineV2+"\n"), 0o600)
				} else {
					_ = os.Remove(pin)
				}
			}
		}
	}()
	return c
}
