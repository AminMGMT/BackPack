package control

import (
	"errors"
	"fmt"
	"strings"

	"github.com/backpack/backpack/internal/node"
)

// Adding a server to the fleet, as a decision rather than as a form handler.
//
// This was three hundred lines into an HTTP handler, mixed with form parsing
// and with ten other actions, and it is the one of them that is not a
// pass-through: it reaches the machine while the operator is still looking at
// the form, installs Backpack when the machine has none, and takes the entry
// back out when it cannot be reached at all.
//
// None of that is about HTTP. It was reachable only through a POST, so a CLI
// that wanted to add a server had to either drive the panel or write the
// sequence again — and a sequence written twice is a sequence where the second
// copy forgets the back-out.
//
// # Why it is reached now rather than on first use
//
// The alternative is to save the details and let the first real operation
// discover that the password is wrong, which is the shape the old flow had and
// is why a server could sit in the fleet doing nothing with nothing saying why.
// A fleet entry that has never worked is not a server, it is a typo — so it is
// removed, and the operator is told which of the three things went wrong.

// Joining is what happened when a server was added.
type Joining struct {
	Info node.Info
	// Installed is whether Backpack was put on the machine as part of joining.
	// The ordinary case for a server somebody has just bought, and worth
	// reporting: it is the difference between "added" and "added and set up".
	Installed bool
}

// JoinError distinguishes the three ways adding a server fails, because they
// have three different fixes and the operator is looking at the form.
type JoinError struct {
	// Stage is "register", "reach" or "install".
	Stage string
	Err   error
}

func (e JoinError) Error() string { return e.Err.Error() }
func (e JoinError) Unwrap() error { return e.Err }

// Join adds a server to the fleet and proves it is reachable, removing it again
// if it is not.
//
// install decides what to do with a machine that answers but has no Backpack on
// it: true installs it, which is the panel's own default because sending the
// operator to a terminal on that machine is the thing managing it was meant to
// avoid.
func (f *Fleet) Join(name, host string, sshPort int, user, password string, install bool) (Joining, error) {
	name = strings.TrimSpace(name)
	host = strings.TrimSpace(host)
	user = strings.TrimSpace(user)
	if user == "" {
		user = "root"
	}
	if sshPort == 0 {
		sshPort = 22
	}
	if sshPort < 1 || sshPort > 65535 {
		return Joining{}, JoinError{Stage: "register",
			Err: fmt.Errorf("choose an SSH port between 1 and 65535")}
	}

	if _, err := node.Add(name, host, sshPort, user, password); err != nil {
		return Joining{}, JoinError{Stage: "register", Err: err}
	}

	run := f.Runner()
	if run == nil {
		_ = node.Remove(name)
		return Joining{}, JoinError{Stage: "reach",
			Err: errors.New("the fleet runner is not started")}
	}

	var out Joining
	err := run.Call(name, node.OpHello, nil, &out.Info)

	// A machine that answers SSH and has no Backpack on it is the ordinary
	// case, not a failure: it is a server the operator has just bought.
	if errors.Is(err, node.ErrNeedsInstall) && install {
		inst, ok := run.(interface{ Install(string) (string, error) })
		if ok {
			if _, ierr := inst.Install(name); ierr != nil {
				_ = node.Remove(name)
				return Joining{}, JoinError{Stage: "install", Err: ierr}
			}
			out.Installed = true
			err = run.Call(name, node.OpHello, nil, &out.Info)
		}
	}
	if err != nil {
		// Taken back out. See the package comment: an entry that has never
		// worked is a typo, and leaving it is how a fleet fills with servers
		// that do nothing and say nothing.
		_ = node.Remove(name)
		return Joining{}, JoinError{Stage: "reach", Err: err}
	}

	_ = node.NoteInfo(name, out.Info)
	return out, nil
}
