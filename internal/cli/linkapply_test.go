package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/manage"
)

// `backpack link apply` is what the one-line install runs on a kharej, so what
// it takes and what it says is the whole of that experience.
func TestLinkApplyTakesTheLinkAndSaysWhetherItConnected(t *testing.T) {
	prevApply, prevAwait, prevRoot := applyLink, awaitLink, isRoot
	t.Cleanup(func() { applyLink, awaitLink, isRoot = prevApply, prevAwait, prevRoot })
	isRoot = func() bool { return true }

	var gotLink string
	var gotOpts manage.LinkApplyOptions
	applyLink = func(raw string, o manage.LinkApplyOptions) (manage.LinkApplied, error) {
		gotLink, gotOpts = raw, o
		return manage.LinkApplied{Name: "client-443", Service: "backpack-client-443.service", Kind: "reverse",
			Transport: "tcp", Dials: "203.0.113.7:443", Backups: []string{"iran.example.com:443"},
			Active: true, RestartHours: 6, RestartMinute: 17}, nil
	}
	awaitLink = func(string, time.Duration) (bool, string) { return true, "" }

	// A link the shell split in two, and both options.
	r := Run([]string{"link", "apply", "--name", "k1", "--host=198.51.100.4", "backpack://abc", "def"})
	if r.Code != CodeOK {
		t.Fatalf("exit %d: %s", r.Code, r.Err)
	}
	if gotLink != "backpack://abcdef" || gotOpts.Name != "k1" || gotOpts.Host != "198.51.100.4" {
		t.Errorf("applied %q with %+v", gotLink, gotOpts)
	}
	for _, want := range []string{`"client-443"`, "203.0.113.7:443", "iran.example.com:443", "every 6 hours at :17 UTC", "Connected"} {
		if !strings.Contains(r.Out, want) {
			t.Errorf("the report does not say %q:\n%s", want, r.Out)
		}
	}

	// Not connected within the wait is reported as such, with an exit a
	// script can act on.
	awaitLink = func(string, time.Duration) (bool, string) { return false, "control channel not up" }
	if r := Run([]string{"link", "apply", "backpack://x"}); r.Code != CodeUnhealthy || !strings.Contains(r.Out, "Not connected yet") {
		t.Errorf("an unconnected tunnel: exit %d\n%s", r.Code, r.Out)
	}
}

func TestLinkApplyRefusesWhatItCannotDo(t *testing.T) {
	prevApply, prevRoot := applyLink, isRoot
	t.Cleanup(func() { applyLink, isRoot = prevApply, prevRoot })
	applyLink = func(string, manage.LinkApplyOptions) (manage.LinkApplied, error) {
		t.Fatal("nothing should have been applied")
		return manage.LinkApplied{}, nil
	}

	isRoot = func() bool { return false }
	if r := Run([]string{"link", "apply", "backpack://x"}); r.Code != CodeFailed || !strings.Contains(r.Err, "root") {
		t.Errorf("not root: exit %d %q", r.Code, r.Err)
	}
	isRoot = func() bool { return true }
	for _, args := range [][]string{{"link"}, {"link", "apply"}, {"link", "apply", "--name"}, {"link", "apply", "--bogus", "x"}, {"link", "show", "x"}} {
		if r := Run(args); r.Code != CodeUsage {
			t.Errorf("%q: exit %d, want usage", args, r.Code)
		}
	}
	if !IsCommand("link") {
		t.Error("main would not route `backpack link` here")
	}
}
