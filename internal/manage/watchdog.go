package manage

import (
	"context"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/alerthist"
)

// Watchdog tuning.
const (
	wdInterval  = 25 * time.Second // how often to check
	wdThreshold = 2                // consecutive unhealthy checks before acting
	wdCooldown  = 3 * time.Minute  // minimum time between restarts of one tunnel
)

// RunWatchdog periodically checks every tunnel and restarts any that is running
// but has lost its tunnel connection (a "dropped" tunnel that the engine didn't
// recover on its own). It works on both ends:
//
//   - server tunnel: unhealthy if no client is connected to its control port
//   - client tunnel: unhealthy if it has no connection to the remote server
//
// A restart is only issued after wdThreshold consecutive unhealthy checks and
// no more than once per wdCooldown, so transient blips and a peer that is
// legitimately down don't cause churn.
func RunWatchdog(ctx context.Context) {
	fails := map[string]int{}
	lastRestart := map[string]time.Time{}
	seenHealthy := map[string]bool{} // only "was up, then dropped" counts as a drop

	ticker := time.NewTicker(wdInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pairs := establishedPairs()
			for _, t := range List() {
				if !IsActive(t.Service) {
					fails[t.Name] = 0 // stopped on purpose (or systemd is restarting a crash)
					continue
				}
				if tunnelHealthy(t, pairs) {
					fails[t.Name] = 0
					seenHealthy[t.Name] = true
					continue
				}
				// Only treat as a "drop" if it had connected before — a tunnel
				// still waiting for its first connection isn't broken.
				if !seenHealthy[t.Name] {
					continue
				}
				fails[t.Name]++
				if fails[t.Name] >= wdThreshold && time.Since(lastRestart[t.Name]) > wdCooldown {
					RestartService(t.Service)
					lastRestart[t.Name] = time.Now()
					// On the record: "why did my tunnel reset overnight" should
					// be answerable from the panel's alert view.
					alerthist.RecordEvent("🔁 Watchdog restarted tunnel " + t.Name +
						" — it was running but not connected")
					fails[t.Name] = 0
				}
			}
		}
	}
}

// tunnelHealthy reports whether a running tunnel currently has its connection up,
// based on the established TCP sockets in `pairs` ([local, peer] address pairs).
func tunnelHealthy(t Tunnel, pairs [][2]string) bool {
	// UDP-based transports (udp, kcp) hold no TCP sockets at all, so the TCP
	// table says nothing about them.
	//
	// A client keeps a connected UDP socket per session, which does show up
	// with its peer, so it can be checked properly. A server's KCP listener is
	// a single unconnected socket that never records who is talking to it —
	// there is nothing to observe, so a running service is reported healthy
	// rather than being restarted forever on the strength of a check that
	// cannot succeed.
	if isDatagram(t.Transport) {
		if t.Role == "server" {
			return true
		}
		pairs = establishedUDPPairs()
	}

	if t.Role == "server" {
		// Healthy if any client is connected to the control (bind) port.
		if _, tport, err := net.SplitHostPort(t.Addr); err == nil {
			for _, p := range pairs {
				if portOf(p[0]) == tport {
					return true
				}
			}
			return false
		}
		return true // can't parse → don't act
	}
	// Client: healthy if connected to the remote server's tunnel port.
	if rhost, rport, err := net.SplitHostPort(t.Addr); err == nil {
		rip := net.ParseIP(rhost)
		for _, p := range pairs {
			ph, pp, err := net.SplitHostPort(p[1])
			if err != nil || pp != rport {
				continue
			}
			// When the configured host is a literal IP, require it to match
			// too — otherwise any unrelated outbound connection to the same
			// port (e.g. 443) would make a dropped tunnel look healthy.
			if rip != nil {
				if pip := net.ParseIP(ph); pip == nil || !pip.Equal(rip) {
					continue
				}
			}
			return true
		}
		return false
	}
	return true
}

// establishedPairs returns [localAddr, peerAddr] for every established TCP socket.
func establishedPairs() [][2]string {
	out, err := exec.Command("ss", "-Htn", "state", "established").Output()
	if err != nil {
		return nil
	}
	var pairs [][2]string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		pairs = append(pairs, [2]string{f[len(f)-2], f[len(f)-1]})
	}
	return pairs
}

// establishedUDPPairs returns [localAddr, peerAddr] for every connected UDP
// socket. A UDP socket only has a peer once it has been connected to one, which
// is exactly what a KCP or UDP client session does — so these are the tunnel's
// own sockets and nobody else's.
func establishedUDPPairs() [][2]string {
	// Deliberately not `ss -u state established`.
	//
	// UDP has no connection state for the kernel to filter on, and iproute2
	// versions disagree about what that filter means for datagram sockets —
	// some return nothing at all. That made a connected KCP client look
	// unconnected, which the health check then reported as "running, but not
	// connected to the server" on a tunnel that was carrying traffic.
	//
	// So every UDP socket is listed and the connected ones are identified by
	// what actually distinguishes them: a real peer address. A socket that has
	// called connect() has one; a listener shows a wildcard.
	out, err := exec.Command("ss", "-Huan").Output()
	if err != nil {
		return nil
	}

	var pairs [][2]string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		local, peer := f[len(f)-2], f[len(f)-1]
		if !hasRealPeer(peer) {
			continue
		}
		pairs = append(pairs, [2]string{local, peer})
	}
	return pairs
}

// hasRealPeer reports whether an `ss` peer column names an actual remote end
// rather than a listening socket's wildcard.
func hasRealPeer(peer string) bool {
	if peer == "" {
		return false
	}
	host, port, err := net.SplitHostPort(peer)
	if err != nil {
		return false
	}
	// A listener renders as *:*, 0.0.0.0:*, or [::]:* depending on the family.
	if port == "*" || port == "0" {
		return false
	}
	switch host {
	case "*", "", "0.0.0.0", "::", "[::]":
		return false
	}
	return true
}

func portOf(hostPort string) string {
	if _, p, err := net.SplitHostPort(hostPort); err == nil {
		return p
	}
	return ""
}
