package manage

import (
	"context"
	"net"
	"os/exec"
	"strings"
	"time"
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
					fails[t.Name] = 0
				}
			}
		}
	}
}

// tunnelHealthy reports whether a running tunnel currently has its connection up,
// based on the established TCP sockets in `pairs` ([local, peer] address pairs).
func tunnelHealthy(t Tunnel, pairs [][2]string) bool {
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
	if _, rport, err := net.SplitHostPort(t.Addr); err == nil {
		for _, p := range pairs {
			if portOf(p[1]) == rport {
				return true
			}
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

func portOf(hostPort string) string {
	if _, p, err := net.SplitHostPort(hostPort); err == nil {
		return p
	}
	return ""
}
