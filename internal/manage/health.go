package manage

import (
	"time"

	"github.com/backpack/backpack/internal/app"
)

// Health describes how a single tunnel is doing right now.
type Health struct {
	Name      string
	Service   string
	Installed bool   // a systemd unit exists for it
	Active    bool   // the service is running
	Connected bool   // the tunnel actually has its peer connection up
	State     string // "online" | "offline" | "stopped"
	Detail    string // human-readable explanation
}

// TunnelHealth reports the live health of one tunnel. "offline" means the
// service runs but the peer is not connected — the case a plain systemd check
// would wrongly call healthy.
func TunnelHealth(t Tunnel) Health {
	return tunnelHealthWith(t, establishedPairs())
}

// tunnelHealthWith computes health reusing an already-collected socket table,
// so checking many tunnels costs a single `ss` call.
func tunnelHealthWith(t Tunnel, pairs [][2]string) Health {
	h := Health{
		Name:      t.Name,
		Service:   t.Service,
		Installed: fileExists(app.ServiceDir + "/" + t.Service),
		Active:    IsActive(t.Service),
	}
	switch {
	case !h.Installed:
		h.State, h.Detail = "stopped", "no systemd unit — the tunnel is not installed"
	case !h.Active:
		h.State, h.Detail = "stopped", "service is not running"
	default:
		h.Connected = tunnelHealthy(t, pairs)
		if h.Connected {
			h.State, h.Detail = "online", "peer connected"
		} else {
			h.State = "offline"
			if t.Role == "server" {
				h.Detail = "running, but no client is connected yet"
			} else {
				h.Detail = "running, but not connected to the server"
			}
		}
	}
	return h
}

// AllHealth returns the health of every tunnel using one socket snapshot.
func AllHealth() []Health {
	pairs := establishedPairs()
	tunnels := List()
	out := make([]Health, 0, len(tunnels))
	for _, t := range tunnels {
		out = append(out, tunnelHealthWith(t, pairs))
	}
	return out
}

// ServiceHealthy reports whether a service is active — the minimum bar used
// after an update or a config change.
func ServiceHealthy(service string) bool { return IsActive(service) }

// WaitServiceActive waits up to timeout for a service to report active,
// polling briefly. Returns true as soon as it is up.
func WaitServiceActive(service string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if IsActive(service) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// WaitTunnelConnected waits up to timeout for a tunnel to have its peer
// connection established (not just the service running).
func WaitTunnelConnected(name string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		for _, t := range List() {
			if t.Name != name {
				continue
			}
			if tunnelHealthWith(t, establishedPairs()).Connected {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Second)
	}
}
