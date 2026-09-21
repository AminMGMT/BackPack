package control

import (
	"log"
	"sync"

	"github.com/backpack/backpack/internal/node"
)

// Fleet owns the connections to the managed servers.
//
// There is no lifetime to manage any more. The channel this replaces had a
// listener per server that had to be opened, moved when its port changed, and
// closed when the server went — three things that could each be wrong on their
// own, and each of which left a server listed here and unreachable. The panel
// dials out now, so the only state worth holding is the connections it is
// reusing, and those look after themselves.
type Fleet struct {
	mu  sync.Mutex
	run node.Runner
}

// Start makes the runner if there is none. It contacts nothing: whether a
// server answers is a question asked when something is asked of it.
func (f *Fleet) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.run == nil {
		f.run = node.NewSSHRunner(func(m string) { log.Printf("fleet: %s", m) })
	}
	return nil
}

func (f *Fleet) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Whatever it is holding open is dropped; an interface that cannot be
	// closed simply has nothing to drop.
	if c, ok := f.run.(interface{ Close() }); ok {
		c.Close()
	}
	f.run = nil
}

func (f *Fleet) Runner() node.Runner {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.run
}

// Runner is the interface the rest of the product sees. Named here as well as
// in internal/node so a caller can depend on this package alone.
type Runner = node.Runner

// Use puts a specific runner behind the fleet, replacing whatever is there.
//
// It exists for the callers that stand in for a fleet of real machines: the
// panel's own tests drive a dozen fleet operations against a runner that
// answers from a map, and reaching into the struct to do it was only possible
// while this type lived in the same package as those tests.
func (f *Fleet) Use(r Runner) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.run = r
}
