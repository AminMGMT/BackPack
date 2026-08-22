package manage

import (
	"net"
	"strings"

	"github.com/backpack/backpack/internal/app"
)

// Is a direct tunnel up?
//
// The watchdog and the health screen both answer this from the kernel's table
// of established TCP sockets, and both used to answer it wrongly for the two
// direct kinds, because the question they ask is phrased in terms of a reverse
// tunnel: a "server" is healthy when something has connected to its bind port,
// and anything else is healthy when it has connected out to its remote.
//
// Neither describes these. A direct tunnel's Iran side dials out, so it looks
// like the reverse tunnel's client and happens to be judged correctly. Its
// kharej side listens, but is not called "server", so it fell through to the
// dial test and could never pass it — a perfectly healthy tunnel showing
// permanently offline. A layer-3 tunnel has no TCP socket at all on any of its
// carriers, so nothing in that table could ever say anything about it.
//
// The watchdog was saved from restarting them in a loop only by its rule that
// a tunnel which has never once been healthy is not treated as having dropped.
// That is a guard against exactly this mistake, not a reason to leave it.

// directHealthy reports whether one of the two direct kinds is up, and whether
// the question could be answered at all. known is false when nothing
// observable can settle it, which callers should read as "leave it alone"
// rather than "it is broken".
func directHealthy(t Tunnel, pairs [][2]string) (healthy, known bool) {
	if strings.HasPrefix(t.Transport, "l3/") {
		// Every layer-3 carrier is a datagram or raw socket: udp holds an
		// unconnected socket, and pck, xdi and spoof do not go through the
		// kernel's stack at all, so the socket table cannot answer for them.
		//
		// The engine can, and writes its peer down for exactly this. Reading
		// it is what turns a layer-3 tunnel from a grey card with no state
		// into one that says whether it is up. A snapshot too old to mean
		// anything still reports unknown, which is the honest answer when the
		// process has stopped writing.
		return datagramPeer(app.ConfigDir, t.Name)
	}
	if !strings.HasPrefix(t.Transport, "direct/") {
		return false, false
	}

	if t.Role == "kharej" {
		// It listens, so it is up when something has connected to its port.
		_, port, err := net.SplitHostPort(t.Addr)
		if err != nil {
			return true, false
		}
		for _, p := range pairs {
			if portOf(p[0]) == port {
				return true, true
			}
		}
		return false, true
	}

	// The Iran side dials out, so it is up when a socket is established to the
	// kharej address it was given. A literal IP is required to match as well,
	// or an unrelated outbound connection to the same port would look like the
	// tunnel.
	host, port, err := net.SplitHostPort(t.Addr)
	if err != nil {
		return true, false
	}
	want := net.ParseIP(host)
	for _, p := range pairs {
		peerHost, peerPort, err := net.SplitHostPort(p[1])
		if err != nil || peerPort != port {
			continue
		}
		if want != nil {
			if got := net.ParseIP(peerHost); got == nil || !got.Equal(want) {
				continue
			}
		}
		return true, true
	}
	return false, true
}

// directStateDetail is the sentence the health screen shows for a direct
// tunnel. The reverse wording — "no client is connected", "not connected to
// the server" — names roles these tunnels do not have.
func directStateDetail(t Tunnel, connected, known bool) string {
	if !known {
		// Only reachable when the process has not written a snapshot yet, or
		// has stopped writing one — a tunnel that has just started, or one
		// whose engine is wedged.
		return "running, but it has not reported its state yet"
	}
	if connected {
		return "peer connected"
	}
	if t.Role == "kharej" {
		return "running, but the Iran server has not connected yet"
	}
	return "running, but not connected to the kharej server"
}
