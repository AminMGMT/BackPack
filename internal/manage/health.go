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

// AllHealth reports the health of every tunnel, keyed by name, from a single
// socket snapshot.
//
// This exists so there is one answer to "is this tunnel up" rather than one per
// caller. The web panel used to work it out itself by looking for peers in the
// TCP socket table, which is correct for the TCP-based transports and silently
// wrong for KCP and UDP: a datagram listener holds no TCP sockets at all, so a
// perfectly healthy KCP tunnel appeared offline. The watchdog already knew
// that; the panel did not, because it was asking a different question in a
// different place.
func AllHealth() map[string]Health {
	pairs := establishedPairs()
	tunnels := List()

	out := make(map[string]Health, len(tunnels))
	for _, t := range tunnels {
		out[t.Name] = tunnelHealthWith(t, pairs)
	}
	return out
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
			h.State = "online"
			if isDatagram(t.Transport) && t.Role == "server" {
				// Be straight about what was actually verified: a UDP listener
				// keeps no record of its peers, so "running" is all we know.
				h.Detail = "running — a UDP listener cannot report its peers"
			} else {
				h.Detail = "peer connected"
			}
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
