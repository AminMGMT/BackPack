package manage

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/alerthist"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/enginectl"
	"github.com/backpack/backpack/internal/metrics"
)

// Watchdog tuning.
const (
	wdInterval  = 25 * time.Second // how often to check
	wdThreshold = 2                // consecutive unhealthy checks before acting
	wdCooldown  = 3 * time.Minute  // minimum time between restarts of one tunnel
)

// Noticing that a tunnel is not failing once but failing repeatedly.
//
// A restart is reported on its own, which is right for a one-off: "why did my
// tunnel reset overnight" should be answerable. It is wrong for a tunnel that
// is doing it all day. At one restart per cooldown a sick tunnel can produce
// twenty lines an hour, and twenty separate lines are indistinguishable from
// twenty unrelated events across a week — nobody reads that list and concludes
// the tunnel is flapping.
//
// Which matters because flapping is how several real faults present. A path
// that drops full-sized packets kills the session on the first real transfer
// and it reconnects; a liveness deadline set too tight tears down a tunnel that
// is merely slow. From the outside all of them look like a tunnel that mostly
// works, which is worse than one that is plainly down: nobody investigates.
//
// So repeated restarts are reported as one condition. The restarting itself
// does not change — a flapping tunnel still gets restarted, because not
// restarting it would only make it a stopped one.
const (
	// flapWindow is how far back the count reaches.
	flapWindow = time.Hour

	// flapThreshold is how many restarts inside that window stop being a
	// coincidence. With the cooldown at three minutes, four restarts span at
	// least nine minutes of trouble.
	flapThreshold = 4
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
	// When each tunnel was restarted, most recent last, pruned to flapWindow.
	restarts := map[string][]time.Time{}
	// When flapping was last reported for a tunnel, so a tunnel that keeps it
	// up for days is reported once a window rather than once and then never.
	lastFlapReport := map[string]time.Time{}
	seenHealthy := map[string]bool{} // only "was up, then dropped" counts as a drop
	// Health by what the tunnel is carrying, alongside health by whether it is
	// connected. See throughputhealth.go for why silence is not the test.
	//
	// Loaded from disk, because a watchdog restart used to reset every ladder
	// mid-climb — including a tunnel it had deliberately given up on, which
	// turned that decision back into a restart loop.
	flow := newFlowWatch()
	flow.load()

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
					flow.forget(t.Name)
					continue
				}
				if tunnelHealthy(t, pairs) {
					fails[t.Name] = 0
					seenHealthy[t.Name] = true
					// Connected is not the same as working. A tunnel that holds
					// its control channel and carries nothing in one direction
					// is the failure this watchdog used to report as healthy.
					checkThroughput(t, flow, lastRestart)
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
					now := time.Now()
					lastRestart[t.Name] = now
					restarts[t.Name] = recentRestarts(restarts[t.Name], now)
					reportRestart(t.Name, restarts[t.Name], lastFlapReport, now)
					// And whether this is one tunnel or the machine. Checked
					// after recording, so the restart that just happened counts.
					reportFleetFlap(restarts, lastFlapReport, now)
					fails[t.Name] = 0
					// And whether it worked. Saying only that a restart
					// happened leaves the operator with the half of the story
					// that worries them and none of the half that would settle
					// it — "it went down" with nothing after it is the most
					// reported complaint about these messages.
					go reportRecovery(t)
				}
			}
			// One write per pass rather than per decision: the file is small,
			// the pass is every 25 seconds, and a crash costs at most one
			// interval of memory.
			flow.save()
		}
	}
}

// recoveryWait is how long a restarted tunnel is given to reconnect before the
// watchdog says whether it did. Long enough for a control channel to be dialled
// and established on a slow path, short enough that the answer still arrives
// while anyone is looking.
const recoveryWait = 45 * time.Second

// reportRecovery follows a watchdog restart with what came of it.
//
// It runs in its own goroutine so the watchdog's own cadence is untouched: this
// waits, and the loop it was started from must not.
func reportRecovery(t Tunnel) {
	deadline := time.Now().Add(recoveryWait)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		if !IsActive(t.Service) {
			continue
		}
		if tunnelHealthy(t, establishedPairs()) {
			alerthist.RecordEvent("🟢 Tunnel " + t.Name + " is carrying traffic again after the restart")
			return
		}
	}
	alerthist.RecordEvent("🔴 Tunnel " + t.Name + " did not come back after the restart — " +
		"it is running but still not connected")
}

// engineSaysConnected asks the tunnel's own engine whether it holds a control
// channel.
//
// This is the answer the socket table cannot give. `ss` reports a socket, and a
// socket outlives the tunnel behind it by a long way: one whose keepalive
// probes go unanswered stays ESTABLISHED for eleven minutes on the shipped
// defaults, and one stalled on a path that drops full-sized packets stays
// ESTABLISHED while it retransmits. Every failure this watchdog missed looked
// healthy in that table, which is what made "the tunnel is down and it had to
// be restarted by hand" a recurring report.
//
// known is false when there is nothing to go on: no snapshot, a snapshot too
// old to mean anything, or one written by a binary from before the engines
// reported this — which is the ordinary case for the minutes after an update,
// while the tunnels are still running the previous version. The caller falls
// back to the socket table then, exactly as it always did.
func engineSaysConnected(name string) (connected, known bool) {
	snap, err := metrics.Read(app.ConfigDir, name)
	if err != nil {
		return false, false
	}
	if time.Since(snap.Taken) > datagramPeerWindow {
		return false, false
	}
	if snap.Connected == nil {
		return false, false
	}
	return *snap.Connected, true
}

// tunnelHealthy reports whether a running tunnel currently has its connection
// up. It prefers what the engine says; `pairs` ([local, peer] address pairs from
// the socket table) is the fallback for the engines that have not said.
func tunnelHealthy(t Tunnel, pairs [][2]string) bool {
	// The direct kinds are judged on their own terms: their roles are
	// geographic, and a layer-3 tunnel has no TCP socket to observe at all.
	// See directHealthy for why the reverse phrasing below cannot answer for
	// them. An unanswerable check reports healthy, because restarting
	// something whose state cannot be seen would mean restarting it forever.
	if IsDirectKind(t) {
		healthy, known := directHealthy(t, pairs)
		return healthy || !known
	}
	// The engine's own answer outranks the socket table wherever there is one.
	if connected, known := engineSaysConnected(t.Name); known {
		return connected
	}
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

// recentRestarts appends now and drops what has fallen out of the window.
func recentRestarts(prev []time.Time, now time.Time) []time.Time {
	out := make([]time.Time, 0, len(prev)+1)
	for _, t := range prev {
		if now.Sub(t) < flapWindow {
			out = append(out, t)
		}
	}
	return append(out, now)
}

// reportRestart puts one restart on the record — as itself while it is still a
// one-off, and as flapping once it is not.
//
// The change at the threshold is deliberate. Going on emitting a line per
// restart would bury the finding under exactly the noise that made it invisible
// in the first place, so past that point the individual lines stop and the
// condition is stated instead, once a window for as long as it lasts.
func reportRestart(name string, at []time.Time, lastReport map[string]time.Time, now time.Time) {
	if len(at) < flapThreshold {
		alerthist.RecordEvent("🔁 Watchdog restarted tunnel " + name +
			" — it was running but not connected")
		return
	}
	if last, ok := lastReport[name]; ok && now.Sub(last) < flapWindow {
		return // already said so this window; the restarts continue regardless
	}
	lastReport[name] = now
	alerthist.RecordEvent(fmt.Sprintf(
		"⚠️ Tunnel %s is flapping — restarted %d times in the last hour. "+
			"It is not failing once, it is failing repeatedly, which usually means the "+
			"path drops full-sized packets (try a TCP MSS clamp) or the link is too "+
			"lossy for the preset. Individual restart alerts for it are suppressed "+
			"while this lasts.", name, len(at)))
}

// checkThroughput asks whether a connected tunnel is actually carrying, and
// walks the graduated response when it is not.
//
// It reports whether it acted. The watchdog does not branch on that — a
// connected tunnel is done either way — but the tests do, because "did the
// ladder take a step" is the whole behaviour.
//
// The restart it may issue goes through the same cooldown as the connection
// watchdog's, so a tunnel cannot be restarted by both halves in the same
// minute — the two are looking at the same tunnel and would otherwise each
// count the other's restart as their own failure to fix it.
func checkThroughput(t Tunnel, flow *flowWatch, lastRestart map[string]time.Time) bool {
	// Layer-3 and direct tunnels keep their own counters differently and are
	// judged on their own terms elsewhere; see directHealthy.
	if IsDirectKind(t) {
		return false
	}
	in, out, ok := tunnelFlow(t.Name)
	if !ok {
		return false
	}
	now := time.Now()
	if flow.observe(t.Name, in, out, now) != flowStalled {
		return false
	}

	action, message := flow.decide(t.Name, now)
	switch action {
	case stallReport, stallGiveUp:
		alerthist.RecordEvent(message)
		return true

	case stallReload:
		// The rung below a process restart: ask the engine to restart its own
		// transport. See internal/enginectl.
		//
		// An engine with no socket falls straight through to the next rung
		// rather than costing the tunnel a wasted interval. That is not a rare
		// case and will not be for a while: the monitor and the engines are
		// separate units and are not updated in the same instant, so a newer
		// watchdog talking to an older engine is the ordinary state of a
		// machine mid-update.
		if err := enginectl.Ask(t.Name, enginectl.OpRestartTransport, nil); err == nil {
			alerthist.RecordEvent(message)
			go reportRecovery(t)
			return true
		}
		fallthrough

	case stallRestart:
		if time.Since(lastRestart[t.Name]) <= wdCooldown {
			return false
		}
		alerthist.RecordEvent(message)
		RestartService(t.Service)
		lastRestart[t.Name] = now
		go reportRecovery(t)
		return true
	}
	return false
}

// Noticing that it is not one tunnel.
//
// flapThreshold is per tunnel, and that is the right unit for the message it
// produces — "try an MSS clamp on this one" is advice about a path. It is the
// wrong unit for the question an operator actually needs answered when several
// tunnels start flapping at once, which is *whether it is the tunnels at all*.
//
// Five tunnels each restarting twice is not five coincidences. It is almost
// always the machine: the clock stepped, the network went away and came back,
// an upgrade left half the fleet on one version, a sysctl was rewritten. None
// of those are fixed by looking at a tunnel, and none of them are visible in
// five separate reports that each say "this one is flapping".
const (
	// fleetFlapTunnels is how many tunnels have to be restarting inside the
	// window before this is reported as one condition. Two is a coincidence;
	// three on one machine is a pattern.
	fleetFlapTunnels = 3

	// fleetFlapEach is how many restarts each of them needs. Deliberately lower
	// than flapThreshold: the point is to notice the pattern *before* any
	// single tunnel has crossed its own bar, because by then the operator has
	// already been sent to look at a path.
	fleetFlapEach = 2
)

// reportFleetFlap says so when several tunnels are restarting at once.
//
// restarts is the same map the per-tunnel reporting reads, so the two cannot
// disagree about what happened. It reports at most once a window, like the
// per-tunnel one, for the same reason: the condition persists and repeating it
// buries everything else.
func reportFleetFlap(restarts map[string][]time.Time, lastReport map[string]time.Time, now time.Time) {
	const key = "" // the fleet itself, which no tunnel can be called

	var affected []string
	for name, at := range restarts {
		if countWithinWindow(at, now) >= fleetFlapEach {
			affected = append(affected, name)
		}
	}
	if len(affected) < fleetFlapTunnels {
		return
	}
	if last, ok := lastReport[key]; ok && now.Sub(last) < flapWindow {
		return
	}
	lastReport[key] = now

	sort.Strings(affected)
	alerthist.RecordEvent(fmt.Sprintf(
		"⚠️ %d tunnels on this server are restarting at once (%s). Several tunnels "+
			"failing together is usually the machine rather than any of them: the clock "+
			"stepped, the network dropped and came back, an update left the two ends on "+
			"different versions, or the socket tuning was rewritten. Check the server "+
			"before looking at any one tunnel.",
		len(affected), strings.Join(affected, ", ")))
}

// countWithinWindow counts restarts still inside the flap window.
//
// Separate from recentRestarts, which *records* one: that helper appends now to
// what it returns, because every caller of it has just restarted something.
// Counting with it would report one restart more than happened, which for a
// threshold of two means reporting a tunnel that has restarted once.
func countWithinWindow(at []time.Time, now time.Time) int {
	n := 0
	for _, t := range at {
		if now.Sub(t) < flapWindow {
			n++
		}
	}
	return n
}
