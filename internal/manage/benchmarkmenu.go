package manage

import (
	"fmt"
	"strings"

	"github.com/backpack/backpack/internal/tui"
)

// LinkTest measures the link to the far server and recommends a transport for
// it. It runs on the side that dials out — the client — because that is the
// side that has a peer address to measure against.
func LinkTest() {
	tui.Clear()
	tui.Title("Link Test")
	tui.Warn("Measures latency, jitter and packet loss to the other server, then")
	tui.Warn("recommends the transport that suits what it finds.")
	fmt.Println()

	clients := clientTunnels()
	if len(clients) == 0 {
		tui.Info("No client tunnels found on this server.")
		tui.Warn("Run this on the abroad (kharej) side — it is the side that dials")
		tui.Warn("out, so it is the side that can measure the link.")
		tui.PressEnter()
		return
	}

	var target Tunnel
	if len(clients) == 1 {
		target = clients[0]
	} else {
		opts := make([]tui.Option, len(clients))
		for i, t := range clients {
			opts[i] = tui.Option{Title: t.Name, Desc: t.Addr + " — " + transportLabel(t.Transport)}
		}
		idx := tui.ChooseOpt("Which tunnel's link should be tested?", opts)
		if idx < 0 {
			return
		}
		target = clients[idx]
	}

	// A UDP-based tunnel cannot be probed by opening a TCP connection to its
	// port: nothing is listening there for TCP, so every probe fails and the
	// link looks dead even while the tunnel is carrying traffic perfectly well.
	// Reporting that as "filtered" — and worse, offering to switch a working
	// tunnel to another transport — would be actively misleading.
	if isDatagram(target.Transport) {
		reportDatagramLink(target)
		return
	}

	fmt.Println()
	tui.Info("Testing the link to " + target.Addr + " — this takes about 10 seconds...")
	fmt.Println()

	q := ProbePath(target.Addr)

	tui.Title("Results")
	fmt.Println()
	if q.Err != nil {
		tui.Error(q.Err.Error())
		tui.PressEnter()
		return
	}

	tui.Info(fmt.Sprintf("  Target        : %s", q.Target))
	tui.Info(fmt.Sprintf("  Probes        : %d sent, %d answered", q.Sent, q.Received))
	if q.Received == 0 {
		tui.Error("  Result        : nothing answered at all")
		fmt.Println()
		tui.Warn("The server is either down, listening on a different port, or the")
		tui.Warn("port is blocked by a firewall. Check those before reading anything")
		tui.Warn("into the numbers.")
		fmt.Println()
	} else {
		tui.Info(fmt.Sprintf("  Latency       : %s average  (best %s, worst %s)",
			shortDur(q.Avg), shortDur(q.Min), shortDur(q.Max)))
		tui.Info(fmt.Sprintf("  Jitter        : ±%s", shortDur(q.Jitter)))
		lossLine := fmt.Sprintf("  Packet loss   : %.0f%%", q.LossPercent())
		if q.LossPercent() >= 2 {
			tui.Error(lossLine)
		} else {
			tui.Info(lossLine)
		}
		fmt.Println()
	}

	// Be explicit about what this test does not tell you, so nobody reads a
	// speed conclusion into a latency measurement.
	tui.Warn("This measures the quality of the link, not its speed. For real")
	tui.Warn("throughput numbers run iperf3 between the two servers.")
	fmt.Println()

	// Liveness timers first: they apply whatever transport is in use, so this
	// is worth offering even when the transport recommendation is "no change".
	if q.Usable() {
		offerKeepAlive(target, q)
	}

	rec := RecommendTransport(q, target.Transport)

	tui.Title("Recommendation: " + rec.Label)
	fmt.Println()
	for _, why := range rec.Why {
		tui.Info("  • " + why)
	}
	for _, c := range rec.Caveats {
		tui.Warn("  ! " + c)
	}
	fmt.Println()

	if rec.Transport == target.Transport {
		tui.PressEnter()
		return
	}

	tui.Warn("Switching transport only works if the OTHER side switches too —")
	tui.Warn("until it does, the tunnel cannot reconnect.")
	fmt.Println()
	if !tui.Confirm(fmt.Sprintf("Switch %q to %s now", target.Name, rec.Label), false) {
		tui.Info("Left unchanged.")
		tui.PressEnter()
		return
	}

	if err := ChangeTransport(target.Name, rec.Transport); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Switched to " + rec.Label + ".")
	tui.Warn("Now switch the server side to " + rec.Label + " as well.")
	tui.PressEnter()
}

// reportDatagramLink explains why a UDP-based tunnel cannot be measured this
// way and shows what is actually known about it instead of inventing a verdict.
func reportDatagramLink(t Tunnel) {
	tui.Title("Results")
	fmt.Println()
	tui.Info("  Target    : " + t.Addr)
	tui.Info("  Transport : " + transportLabel(t.Transport))
	fmt.Println()
	tui.Warn("This tunnel runs over UDP, and a UDP port cannot be tested by opening")
	tui.Warn("a connection to it the way a TCP port can — a working port and a")
	tui.Warn("blocked one look exactly the same from here. So there is no honest")
	tui.Warn("latency or loss number to give you for this tunnel.")
	fmt.Println()

	h := TunnelHealth(t)
	tui.Title("What is actually known")
	fmt.Println()
	switch h.State {
	case "online":
		tui.Success("  The tunnel is up and carrying traffic.")
		tui.Info("  " + h.Detail)
		fmt.Println()
		tui.Info("Since it works, there is nothing here to fix. If you want real")
		tui.Info("numbers for this link, run iperf3 between the two servers.")
	case "offline":
		tui.Error("  The service is running but the tunnel is not connected.")
		tui.Info("  " + h.Detail)
		fmt.Println()
		tui.Warn("Check, in this order:")
		tui.Warn("  1. the other side is running and uses the SAME transport and preset")
		tui.Warn("  2. UDP " + addrPort(t.Addr) + " is open on the server firewall")
		tui.Warn("     (ufw allow " + addrPort(t.Addr) + "/udp — note the /udp, TCP is not enough)")
		tui.Warn("  3. the token matches on both sides")
	default:
		tui.Error("  The tunnel service is not running.")
		tui.Info("  " + h.Detail)
	}
	fmt.Println()
	tui.Warn("To compare transports, run this test on a TCP-based tunnel — or")
	tui.Warn("temporarily switch this one to TCP Mux and test that.")
	tui.PressEnter()
}

// offerKeepAlive proposes liveness timers derived from the measurement and
// applies them if the user agrees.
func offerKeepAlive(t Tunnel, q PathQuality) {
	spec, err := LoadSpec(t.Name)
	if err != nil {
		return
	}
	plan := RecommendKeepAlive(q)
	if spec.KeepAlive == plan.KeepAlive && spec.Heartbeat == plan.Heartbeat {
		return // already tuned for this link
	}

	tui.Title("Liveness timers")
	fmt.Println()
	tui.Info(fmt.Sprintf("  Now       : keepalive %ds, heartbeat %ds", spec.KeepAlive, spec.Heartbeat))
	tui.Info(fmt.Sprintf("  Suggested : keepalive %ds, heartbeat %ds", plan.KeepAlive, plan.Heartbeat))
	tui.Warn("  " + plan.Why)
	fmt.Println()
	if plan.Heartbeat < spec.Heartbeat {
		tui.Info("Tighter timers notice a dropped tunnel sooner on a link this steady.")
	} else {
		tui.Info("Looser timers stop a slow-but-alive peer from being declared dead.")
	}
	fmt.Println()

	if !tui.Confirm("Apply these timers to "+t.Name, false) {
		return
	}
	if err := SetKeepAlive(t.Name, plan); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Timers applied and the tunnel restarted.")
	tui.Warn("Set the same values on the OTHER side so both agree.")
	fmt.Println()
}

// clientTunnels returns only the tunnels that dial out.
func clientTunnels() []Tunnel {
	var out []Tunnel
	for _, t := range List() {
		if t.Role == "client" && strings.TrimSpace(t.Addr) != "" {
			out = append(out, t)
		}
	}
	return out
}
