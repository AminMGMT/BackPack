package manage

import (
	"fmt"
	"time"

	"github.com/backpack/backpack/internal/app"
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

	// ServiceDown is set when the tunnel is up and the service it forwards to
	// is not: every connection is delivered and then refused one hop past the
	// end of the tunnel.
	//
	// It is deliberately not a State. The tunnel really is online — the control
	// channel is held, the peer is there, and restarting it would fix nothing
	// and drop whatever else it carries. The watchdog reads State and must go
	// on reading "online" here. What was missing is that an operator reading
	// the same answer had no way to tell this apart from a tunnel that works,
	// and the only record of it was a line in the client's log on the other
	// machine.
	ServiceDown *ServiceDownDetail
}

// ServiceDownDetail names the last hop that is failing.
type ServiceDownDetail struct {
	Addr     string    // the address the client is dialling
	Why      string    // "refused" | "timeout" | "unreachable"
	Failures uint64    // connections lost this way since the last that worked
	Since    time.Time // when the run started
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
		//
		// Both roles, not just the server. The dialling side was left to the
		// socket table, which can only see the carriers that leave a socket
		// behind: plain kcp, udp and quic dial a connected UDP socket, but xdi
		// rides in ICMP, pck builds its own TCP segments through a packet
		// socket and spoof sends from a raw one. For those the table has
		// nothing to report and said so, which is how a tunnel that was
		// carrying traffic showed offline on one machine and online on the
		// other. Both ends write the peer down now, so both are asked.
		if isDatagram(t.Transport) {
			if connected, known := datagramPeer(app.ConfigDir, t.Name); known {
				h.Connected = connected
			}
		}

		// The direct kinds get their own answer and their own wording. Here,
		// unlike in the watchdog, a check that cannot see the tunnel says so
		// rather than showing green — a light that is on because nothing
		// looked is worse than one that admits it does not know.
		if IsDirectKind(t) {
			connected, known := directHealthy(t, pairs)
			h.Connected = connected && known
			h.Detail = directStateDetail(t, connected, known)
			h.State = "offline"
			if h.Connected {
				h.State = "online"
			} else if !known {
				h.State = "unknown"
			}
			return h
		}

		if h.Connected {
			h.State = "online"
			h.Detail = "peer connected"
			// A tunnel that is up and delivering into nothing.
			if d := serviceDown(app.ConfigDir, t.Name); d != nil {
				h.ServiceDown = d
				h.Detail = serviceDownDetail(d)
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

// datagramPeerWindow is how long a snapshot's peer field stays meaningful. The
// transport rewrites the file every 30 seconds, so anything much older than a
// few intervals says nothing about now.
const datagramPeerWindow = 90 * time.Second

// datagramPeer reports whether a datagram tunnel currently has a peer, and
// whether that could be determined at all. It answers for either end.
//
// A UDP listener is one unconnected socket and keeps no record of who is
// talking to it, so the kernel cannot answer this — which is why it used to go
// unanswered. Nor can it answer for a dialling side whose carrier opens no
// socket the kernel knows about, which is every carrier that exists to leave
// nothing observable behind. The transport can answer in both cases, and
// records the peer in the tunnel's metrics snapshot, clearing it when the
// control channel drops.
//
// Not knowing is reported separately from knowing there is nobody there. A
// tunnel that has only just started has not written a snapshot yet, and calling
// that "no peer" would show every freshly started tunnel as down for its first
// half minute.
func datagramPeer(dir, name string) (connected, known bool) {
	snap, err := metrics.Read(dir, name)
	if err != nil {
		return false, false // no snapshot yet
	}
	if time.Since(snap.Taken) > datagramPeerWindow {
		return false, false // too old to mean anything either way
	}
	return snap.Peer != "", true
}

// serviceDown reads what the client wrote about its last hop, when that hop is
// failing and the record is recent enough to describe now.
//
// Only the dialling side has this to say: it is the end that hands each
// connection to the service being forwarded to. On the listening side the
// snapshot carries nothing here and this returns nil, which is correct — that
// machine genuinely does not know.
func serviceDown(dir, name string) *ServiceDownDetail {
	snap, err := metrics.Read(dir, name)
	if err != nil || snap.LocalService == nil {
		return nil
	}
	if time.Since(snap.Taken) > datagramPeerWindow {
		return nil // too old to describe now
	}
	ls := snap.LocalService
	return &ServiceDownDetail{
		Addr: ls.Addr, Why: ls.Why, Failures: ls.Failures, Since: ls.Since,
	}
}

// serviceDownDetail is the sentence shown beside a tunnel in this state. It
// says which machine, which address and how many connections, because those
// three are what separate "the service is down" from "one connection lost a
// race during a restart".
func serviceDownDetail(d *ServiceDownDetail) string {
	what := "is not answering"
	switch d.Why {
	case "refused":
		what = "is not listening"
	case "timeout":
		what = "is not answering — a firewall on that machine, or a wedged service"
	}
	return fmt.Sprintf("the tunnel is up, but %s on the far server %s: %d connection(s) refused since %s",
		d.Addr, what, d.Failures, d.Since.Format("15:04"))
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
