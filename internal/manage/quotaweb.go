package manage

import (
	"fmt"
	"strings"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/quota"
)

// A tunnel's traffic limit, as the panel sets and reads it. The engine is what
// enforces it — see cmd/quota.go — so all this does is write the one file.

// TunnelQuota is a tunnel's limit and what it has used against it.
type TunnelQuota struct {
	Name  string `json:"name"`
	Limit uint64 `json:"limit"` // bytes; 0 is no limit
	Used  uint64 `json:"used"`  // bytes, in and out, since the tunnel was made
	Hit   bool   `json:"hit"`   // used has reached the limit: the tunnel is offline
	// Settable is false on the kharej end, which follows whatever the Iran end
	// lets through and is not where a limit belongs.
	Settable bool `json:"settable"`
}

// ReadTunnelQuota reports a tunnel's limit and its use.
func ReadTunnelQuota(name string) (TunnelQuota, error) {
	t, ok := Find(name)
	if !ok {
		return TunnelQuota{}, fmt.Errorf("no tunnel named %q", name)
	}
	q, err := quota.Load(app.ConfigDir, name)
	if err != nil {
		return TunnelQuota{}, fmt.Errorf("the traffic limit of %s cannot be read: %w", name, err)
	}
	out := TunnelQuota{Name: name, Limit: q.Limit, Settable: HoldsPorts(t)}
	if s, err := metrics.Read(app.ConfigDir, name); err == nil {
		out.Used = s.BytesIn + s.BytesOut
	}
	out.Hit = q.Reached(out.Used)
	return out, nil
}

// SetTunnelQuota sets a tunnel's limit; zero removes it. Only on the Iran end,
// the one the users connect to: a limit there stops the traffic at the door,
// and the kharej has nothing to count that the Iran end does not.
func SetTunnelQuota(name string, limit uint64) (TunnelQuota, error) {
	t, ok := Find(name)
	if !ok {
		return TunnelQuota{}, fmt.Errorf("no tunnel named %q", name)
	}
	if !HoldsPorts(t) {
		return TunnelQuota{}, fmt.Errorf("%s is the kharej end — a traffic limit is set on the Iran end of a tunnel", name)
	}
	if err := quota.Save(app.ConfigDir, name, limit); err != nil {
		return TunnelQuota{}, fmt.Errorf("could not save the traffic limit: %w", err)
	}
	return ReadTunnelQuota(name)
}

// ResetTunnelUsage sets a tunnel's carried total back to zero (#106).
//
// The running engine keeps the total in memory and writes it back every few
// seconds, so removing the file under it would only last until the next
// write. The tunnel is stopped first, so its last write is in, and started
// again afterwards if it was running: a short reconnect, which is the price of
// a count the engine cannot quietly restore. What it had carried moves to the
// server's ledger, the way a delete does, so the server's lifetime figure is
// unchanged. A tunnel held offline by its limit comes back with the reset.
func ResetTunnelUsage(name string) (TunnelQuota, error) {
	if _, ok := Find(name); !ok {
		return TunnelQuota{}, fmt.Errorf("no tunnel named %q", name)
	}
	if err := resetUsageIn(app.ConfigDir, name); err != nil {
		return TunnelQuota{}, err
	}
	return ReadTunnelQuota(name)
}

// ResetAllUsage resets every tunnel's carried total, one tunnel at a time so
// only one is reconnecting at once. It goes on past a failure and reports the
// tunnels that could not be reset.
func ResetAllUsage() (reset []string, err error) {
	var names []string
	for _, t := range List() {
		names = append(names, t.Name)
	}
	return resetAllIn(app.ConfigDir, names)
}

func resetAllIn(dir string, names []string) (reset []string, err error) {
	var failed []string
	for _, name := range names {
		if e := resetUsageIn(dir, name); e != nil {
			failed = append(failed, name+": "+e.Error())
			continue
		}
		reset = append(reset, name)
	}
	if len(failed) > 0 {
		return reset, fmt.Errorf("%d tunnel(s) were not reset:\n  %s", len(failed), strings.Join(failed, "\n  "))
	}
	return reset, nil
}

func resetUsageIn(dir, name string) error {
	service := app.ServiceName(name)
	wasActive := IsActive(service)
	if wasActive {
		if err := StopService(service); err != nil {
			return fmt.Errorf("could not stop %s to reset its usage: %w", name, err)
		}
	}
	retireErr := metrics.Retire(dir, name)
	if wasActive {
		if err := StartService(service); err != nil {
			return fmt.Errorf("the usage of %s was reset, but it did not start again: %w", name, err)
		}
	}
	if retireErr != nil {
		return fmt.Errorf("could not reset the usage of %s: %w", name, retireErr)
	}
	return nil
}
