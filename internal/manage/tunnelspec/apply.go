package tunnelspec

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage/core"
)

// Apply writes a changed tunnel config, restarts the service and verifies
// it actually came back up. If it does not, the previous config is put back and
// the tunnel restarted with it, so a bad edit (a port already in use, a wrong
// address) can never leave the user with a dead tunnel and a lost config.
func Apply(s Spec) error {
	// Helper refusals happen before any mutation; do not enter rollback and
	// replace the current file for an edit that was never written.
	if err := s.validateHelpers(); err != nil {
		return err
	}
	path := app.ConfigPath(s.Name)
	service := app.ServiceName(s.Name)

	prev, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("could not read the current config: %w", err)
	}
	wasActive := core.IsActive(service)

	if _, err := s.Save(); err != nil {
		// Save failed — put the original file back untouched.
		_ = app.WriteFileAtomic(path, prev, app.TunnelConfigMode)
		return err
	}
	// Save alone won't reload an already-running unit — restart explicitly.
	if err := core.RestartService(service); err != nil {
		Revert(path, prev, service, wasActive)
		return fmt.Errorf("the tunnel failed to restart with the new settings — reverted: %w", err)
	}

	// The unit can report "activating" for a moment; give it a chance, then
	// confirm it is really running.
	if !core.WaitServiceActive(service, 10*time.Second) {
		detail := LastLogLine(service)
		Revert(path, prev, service, wasActive)
		if detail != "" {
			return fmt.Errorf("the tunnel did not come up with the new settings — reverted. Reason: %s", detail)
		}
		return fmt.Errorf("the tunnel did not come up with the new settings — reverted to the previous config")
	}
	// Filed only now. A change that was reverted above replaced nothing, and
	// recording it would put the configuration that is still running into the
	// list of ones to go back to. See confhist.go.
	RecordChange(s.Name, prev, "")
	return nil
}

// applyRaw writes a configuration verbatim and restarts the tunnel on it,
// with the same revert-if-it-will-not-start guarantee Apply gives.
//
// Verbatim is the point: restoring by re-rendering a spec would silently drop
// any key the current spec cannot hold, which is the trap DirectSpec's own
// comment warns about. What was kept is what goes back.
// ApplyBody is applyRaw for a caller that has rendered the configuration
// itself: written verbatim, restarted, put back if it does not come up.
func ApplyBody(name, body, note string) error { return applyRaw(name, []byte(body), note) }

func applyRaw(name string, cfg []byte, note string) error {
	path := app.ConfigPath(name)
	service := app.ServiceName(name)

	prev, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("could not read the current config: %w", err)
	}
	wasActive := core.IsActive(service)

	if err := app.WriteFileAtomic(path, cfg, app.TunnelConfigMode); err != nil {
		_ = app.WriteFileAtomic(path, prev, app.TunnelConfigMode)
		return err
	}
	if err := core.RestartService(service); err != nil {
		Revert(path, prev, service, wasActive)
		return fmt.Errorf("the tunnel failed to restart on that configuration — reverted: %w", err)
	}
	if !core.WaitServiceActive(service, 10*time.Second) {
		detail := LastLogLine(service)
		Revert(path, prev, service, wasActive)
		if detail != "" {
			return fmt.Errorf("the tunnel did not come up on that configuration — reverted. Reason: %s", detail)
		}
		return fmt.Errorf("the tunnel did not come up on that configuration — reverted")
	}
	RecordChange(name, prev, note)
	return nil
}

// Revert restores a previous config file and brings the service back to the
// state it was in before the edit.
func Revert(path string, prev []byte, service string, wasActive bool) {
	_ = app.WriteFileAtomic(path, prev, app.TunnelConfigMode)
	if wasActive {
		_ = core.RestartService(service)
	} else {
		_ = core.StopService(service)
	}
}

// LastLogLine returns the most recent meaningful journal line for a service,
// used to explain why an edit was reverted.
func LastLogLine(service string) string {
	out, err := exec.Command("journalctl", "-u", service, "-n", "12", "--no-pager", "-o", "cat").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		low := strings.ToLower(l)
		if strings.Contains(low, "error") || strings.Contains(low, "fatal") ||
			strings.Contains(low, "failed") || strings.Contains(low, "in use") {
			return l
		}
	}
	return ""
}
