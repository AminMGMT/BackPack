package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/manage"
)

// `backpack tunnel reset-usage` restarts tunnels, so it asks for root, says
// which tunnels it reset, and reports the ones it could not (#106).
func TestTunnelResetUsage(t *testing.T) {
	prevOne, prevAll, prevRoot := resetUsage, resetAllUsage, isRoot
	t.Cleanup(func() { resetUsage, resetAllUsage, isRoot = prevOne, prevAll, prevRoot })

	resetUsage = func(string) (manage.TunnelQuota, error) {
		t.Fatal("reset a tunnel it should not have")
		return manage.TunnelQuota{}, nil
	}
	isRoot = func() bool { return false }
	if r := Run([]string{"tunnel", "reset-usage", "--all"}); r.Code != CodeFailed || !strings.Contains(r.Err, "root") {
		t.Errorf("without root: exit %d, %q", r.Code, r.Err)
	}

	isRoot = func() bool { return true }
	if r := Run([]string{"tunnel", "reset-usage"}); r.Code != CodeUsage {
		t.Errorf("no target: exit %d", r.Code)
	}
	if r := Run([]string{"tunnel", "reset-usage", "--every"}); r.Code != CodeUsage {
		t.Errorf("an unknown flag: exit %d", r.Code)
	}

	resetAllUsage = func() ([]string, error) {
		return []string{"iran-443"}, errors.New("1 tunnel(s) were not reset:\n  iran-8443: stop failed")
	}
	r := Run([]string{"tunnel", "reset-usage", "--all"})
	if r.Code != CodeFailed || !strings.Contains(r.Out, "iran-443 starts counting from zero") || !strings.Contains(r.Err, "iran-8443") {
		t.Errorf("a partial reset: exit %d\nout: %s\nerr: %s", r.Code, r.Out, r.Err)
	}

	resetAllUsage = func() ([]string, error) { return nil, nil }
	if r := Run([]string{"tunnel", "reset-usage", "--all"}); r.Code != CodeOK || !strings.Contains(r.Out, "No tunnels") {
		t.Errorf("nothing to reset: exit %d, %q", r.Code, r.Out)
	}
}
