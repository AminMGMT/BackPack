package manage

import (
	"fmt"

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
