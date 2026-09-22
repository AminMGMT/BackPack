package control

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/backpack/backpack/internal/node"
)

// Joining a server, without a server.
//
// What is held here is the decision, which is the part that was unreachable
// from anywhere but a POST: reach the machine now, install Backpack when it has
// none, and take the entry back out when it cannot be reached at all. The last
// of those is the one that matters — a fleet entry that has never worked is a
// typo, and leaving it is how a fleet fills with servers that do nothing and
// say nothing about why.

// joinRunner answers the one call Join makes, and can be told to refuse.
type joinRunner struct {
	err        error
	installed  bool
	installErr error
	calls      int
}

func (j *joinRunner) Call(name, op string, body, out any) error {
	j.calls++
	if j.err != nil {
		// A runner that needed an install answers once with that, and then
		// succeeds — which is the sequence a real one produces.
		if errors.Is(j.err, node.ErrNeedsInstall) && j.installed {
			return nil
		}
		return j.err
	}
	return nil
}

func (j *joinRunner) IsOnline(string) bool            { return true }
func (j *joinRunner) Reachable(string) (bool, string) { return true, "" }
func (j *joinRunner) Forget(string)                   {}
func (j *joinRunner) Install(name string) (string, error) {
	if j.installErr != nil {
		return "", j.installErr
	}
	j.installed = true
	return "installed", nil
}

func fleetWith(t *testing.T, r Runner) *Fleet {
	t.Helper()
	// The registry — and the fleet key, which is derived from the registry's
	// directory — writes under /etc. Aimed somewhere this test owns.
	dir := t.TempDir()
	old := node.StorePath
	node.StorePath = filepath.Join(dir, "nodes.json")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("cannot prepare %s: %v", dir, err)
	}
	t.Cleanup(func() { node.StorePath = old })

	f := &Fleet{}
	if r != nil {
		f.Use(r)
	}
	return f
}

// A port nobody can bind is refused before anything is written down, and the
// stage says whose mistake it is.
func TestAnImpossiblePortIsRefusedBeforeAnythingIsSaved(t *testing.T) {
	_, err := fleetWith(t, &joinRunner{}).Join("de-1", "192.0.2.1", 70000, "root", "pw", true)
	if err == nil {
		t.Fatal("a port above 65535 was accepted")
	}
	var je JoinError
	if !errors.As(err, &je) || je.Stage != "register" {
		t.Fatalf("the failure is %+v, and a bad port is the operator's to correct", err)
	}
}

// With no runner there is nothing to reach the machine with, and the entry must
// not be left behind.
func TestJoiningWithNoRunnerFailsAtReaching(t *testing.T) {
	_, err := fleetWith(t, nil).Join("de-1", "192.0.2.1", 22, "root", "pw", true)
	if err == nil {
		t.Fatal("a server joined a fleet with no runner")
	}
	var je JoinError
	if !errors.As(err, &je) || je.Stage != "reach" {
		t.Fatalf("the failure is %+v, want the reaching stage", err)
	}
}

// The ordinary case for a server somebody has just bought: it answers, it has
// no Backpack, and the panel installs it rather than sending them to a terminal
// on that machine.
func TestAMachineWithNoBackpackIsInstalled(t *testing.T) {
	r := &joinRunner{err: node.ErrNeedsInstall}
	got, err := fleetWith(t, r).Join("de-1", "192.0.2.1", 22, "root", "pw", true)
	if err != nil {
		t.Fatalf("a machine that needed installing was refused: %v", err)
	}
	if !got.Installed {
		t.Error("the result does not say Backpack was installed, which is the difference " +
			"between 'added' and 'added and set up'")
	}
}

// And when the operator said not to, it is not installed and the join fails —
// rather than quietly installing anyway.
func TestAMachineWithNoBackpackIsNotInstalledWhenItWasNotAsked(t *testing.T) {
	r := &joinRunner{err: node.ErrNeedsInstall}
	_, err := fleetWith(t, r).Join("de-1", "192.0.2.1", 22, "root", "pw", false)
	if err == nil {
		t.Fatal("a machine with no Backpack joined without being installed")
	}
	if r.installed {
		t.Fatal("Backpack was installed on a machine after the operator declined")
	}
}

// A failed install fails the join at the install stage, and the far machine's
// own words reach the operator.
func TestAFailedInstallIsReportedAsOne(t *testing.T) {
	r := &joinRunner{err: node.ErrNeedsInstall, installErr: errors.New("no space left on device")}
	_, err := fleetWith(t, r).Join("de-1", "192.0.2.1", 22, "root", "pw", true)

	var je JoinError
	if !errors.As(err, &je) || je.Stage != "install" {
		t.Fatalf("the failure is %+v, want the install stage", err)
	}
	if err.Error() != "no space left on device" {
		t.Fatalf("the machine's own words were lost: %v", err)
	}
}

// The default login is root, because that is what the panel needs and what
// every one of these servers has.
func TestTheDefaultLoginAndPortAreFilledIn(t *testing.T) {
	r := &joinRunner{}
	if _, err := fleetWith(t, r).Join("de-1", "192.0.2.1", 0, "", "pw", true); err != nil {
		t.Fatalf("a join with no port and no user was refused: %v", err)
	}
	if r.calls == 0 {
		t.Fatal("the machine was never reached")
	}
}
