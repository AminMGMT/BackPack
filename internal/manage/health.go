package manage

import (
	"context"
	"strings"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/engine"
	"github.com/backpack/backpack/internal/metrics"
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
		if t.Engine == string(config.EngineIPTables) {
			cfg, err := config.LoadFile(app.ConfigPath(t.Name))
			if err != nil {
				h.State, h.Detail = "offline", "configuration is unreadable: "+err.Error()
				return h
			}
			p, err := engine.Resolve(cfg)
			if err != nil {
				h.State, h.Detail = "offline", err.Error()
				return h
			}
			eh, err := p.Health(context.Background(), engine.Request{ConfigPath: app.ConfigPath(t.Name), Config: cfg})
			if err != nil || !eh.Ready {
				h.State = "offline"
				if err != nil {
					h.Detail = err.Error()
				} else {
					h.Detail = eh.Detail + ": " + strings.Join(eh.Drift, "; ")
				}
				return h
			}
			h.Connected, h.State, h.Detail = true, "online", eh.Detail
			return h
		}
		h.Connected = tunnelHealthy(t, pairs)
		// tunnelHealthy answers the watchdog's question — "is this worth
		// restarting?" — and for a datagram server it deliberately says yes
		// always, because restarting something whose peer cannot be observed
		// would mean restarting it forever.
		//
		// That is the right answer for the watchdog and the wrong one here. It
		// left a KCP or UDP server showing green after its client had been
		// stopped: the peer and the ping both vanished, because those come from
		// somewhere that knew the truth, while the light stayed on because this
		// did not. The transport does know, and writes it down — so ask that.
		if isDatagram(t.Transport) && t.Role == "server" {
			if connected, known := datagramServerPeer(app.ConfigDir, t.Name); known {
				h.Connected = connected
			}
		}
		if t.AppForward() && isRawDatagram(t.Transport) && t.Role == "client" {
			if connected, known := datagramServerPeer(app.ConfigDir, t.Name); known {
				h.Connected = connected
			} else {
				h.Connected = false
			}
		}
		if h.Connected {
			h.State = "online"
			h.Detail = "peer connected"
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

// datagramPeerWindow is how long a snapshot's peer field stays meaningful. The
// transport rewrites the file every 30 seconds, so anything much older than a
// few intervals says nothing about now.
const datagramPeerWindow = 90 * time.Second

// datagramServerPeer reports whether a datagram server currently has a peer,
// and whether that could be determined at all.
//
// A UDP listener is one unconnected socket and keeps no record of who is
// talking to it, so the kernel cannot answer this — which is why it used to go
// unanswered. The transport can, and records the peer in the tunnel's metrics
// snapshot, clearing it when the control channel drops.
//
// Not knowing is reported separately from knowing there is nobody there. A
// tunnel that has only just started has not written a snapshot yet, and calling
// that "no peer" would show every freshly started tunnel as down for its first
// half minute.
func datagramServerPeer(dir, name string) (connected, known bool) {
	snap, err := metrics.Read(dir, name)
	if err != nil {
		return false, false // no snapshot yet
	}
	if time.Since(snap.Taken) > datagramPeerWindow {
		return false, false // too old to mean anything either way
	}
	return snap.Peer != "", true
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
