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
	tui.Warn("Latency, Jitter And Loss To The Other Server, And A Transport For It.")
	fmt.Println()

	clients := clientTunnels()
	if len(clients) == 0 {
		tui.Info("No Kharej-Side Tunnels Here — Run This On The Kharej.")
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
		idx := tui.ChooseOpt("Tunnel To Test", opts)
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
	tui.Info("Testing " + target.Addr + " (About 10s)...")
	fmt.Println()

	q := ProbePath(target.Addr)

	tui.Title("Results")
	fmt.Println()
	if q.Err != nil {
		tui.Error(q.Err.Error())
		tui.PressEnter()
		return
	}

	tui.Info(fmt.Sprintf("  Target  : %s", q.Target))
	tui.Info(fmt.Sprintf("  Probes  : %d Sent, %d Answered", q.Sent, q.Received))
	if q.Received == 0 {
		tui.Error("  Result  : Nothing Answered")
		fmt.Println()
		tui.Warn("Server Down, Wrong Port, Or Firewall.")
		fmt.Println()
	} else {
		tui.Info(fmt.Sprintf("  Latency : %s Avg (Best %s, Worst %s)",
			shortDur(q.Avg), shortDur(q.Min), shortDur(q.Max)))
		tui.Info(fmt.Sprintf("  Jitter  : ±%s", shortDur(q.Jitter)))
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
	tui.Warn("Quality, Not Speed — For Speed Use Connection Test.")
	fmt.Println()

	// Liveness timers first: they apply whatever transport is in use, so this
	// is worth offering even when the transport recommendation is "no change".
	if q.Usable() {
		offerKeepAlive(target, q)
	}

	rec := RecommendTransport(q, target.Transport, ProbeUDPEgress())

	tui.Title("Recommendation: " + rec.Label)
	fmt.Println()
	for _, why := range rec.Why {
		tui.Info("  • " + why)
	}
	if rec.FEC.Set() {
		tui.Success(fmt.Sprintf("  ▸ FEC %s — %s", rec.FEC.Ratio(), rec.FEC.Why))
		tui.Warn("    Same Ratio On Both Ends.")
	}
	for _, c := range rec.Caveats {
		tui.Warn("  ! " + c)
	}
	fmt.Println()

	if rec.Transport == target.Transport {
		// Same transport, but the measured FEC may still beat what it runs now.
		if rec.FEC.Set() {
			offerFEC(target, rec.FEC)
		}
		tui.PressEnter()
		return
	}

	tui.Warn("The Other Side Must Switch Too.")
	fmt.Println()
	if !tui.Confirm(fmt.Sprintf("Switch To %s", rec.Label), false) {
		tui.Info("Unchanged.")
		tui.PressEnter()
		return
	}

	if err := ChangeTransport(target.Name, rec.Transport); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Switched To " + rec.Label + ".")
	// A fresh KCP tunnel starts on its preset's FEC; nudge it to the ratio the
	// measurement actually calls for so the switch lands fully tuned.
	if rec.FEC.Set() {
		if err := SetFEC(target.Name, rec.FEC); err == nil {
			tui.Success("FEC " + rec.FEC.Ratio() + " Applied.")
		}
	}
	tui.Warn("Now switch the server side to " + rec.Label + " as well" +
		func() string {
			if rec.FEC.Set() {
				return ", with the same FEC " + rec.FEC.Ratio() + "."
			}
			return "."
		}())
	tui.PressEnter()
}

// offerFEC proposes a measured parity ratio for a KCP tunnel and applies it if
// the operator agrees. SetFEC refuses a no-op, so an unchanged ratio just falls
// through quietly.
func offerFEC(t Tunnel, plan FECPlan) {
	spec, err := LoadSpec(t.Name)
	if err != nil {
		return
	}
	if spec.KCPDataShards == plan.Data && spec.KCPParityShards == plan.Parity {
		return // already tuned for this loss
	}

	tui.Title("Error Correction")
	fmt.Println()
	tui.Info(fmt.Sprintf("  Now       : FEC %d:%d", spec.KCPDataShards, spec.KCPParityShards))
	tui.Info(fmt.Sprintf("  Suggested : FEC %s", plan.Ratio()))
	tui.Warn("  " + plan.Why)
	fmt.Println()

	if !tui.Confirm("Apply FEC "+plan.Ratio(), false) {
		return
	}
	if err := SetFEC(t.Name, plan); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("FEC Applied — Restarted.")
	tui.Warn("Set " + plan.Ratio() + " On The Other Side Too.")
	fmt.Println()
}

// reportDatagramLink explains why a UDP-based tunnel cannot be measured this
// way and shows what is actually known about it instead of inventing a verdict.
func reportDatagramLink(t Tunnel) {
	tui.Title("Results")
	fmt.Println()
	tui.Info("  Target    : " + t.Addr)
	tui.Info("  Transport : " + transportLabel(t.Transport))
	fmt.Println()
	tui.Warn("A UDP Port Cannot Be Probed From Here — Use Connection Test.")
	fmt.Println()

	h := TunnelHealth(t)
	tui.Title("Status")
	fmt.Println()
	switch h.State {
	case "online":
		tui.Success("  Up And Carrying Traffic.")
		tui.Info("  " + h.Detail)
		fmt.Println()

	case "offline":
		tui.Error("  Running But Not Connected.")
		tui.Info("  " + h.Detail)
		fmt.Println()
		tui.Warn("Check: Same Transport/Preset/Token, And ufw allow " + addrPort(t.Addr) + "/udp On The Iran Side.")
	default:
		tui.Error("  Service Not Running.")
		tui.Info("  " + h.Detail)
	}
	fmt.Println()

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

	tui.Title("Liveness Timers")
	fmt.Println()
	tui.Info(fmt.Sprintf("  Now       : Keepalive %ds, Heartbeat %ds", spec.KeepAlive, spec.Heartbeat))
	tui.Info(fmt.Sprintf("  Suggested : Keepalive %ds, Heartbeat %ds", plan.KeepAlive, plan.Heartbeat))
	tui.Warn("  " + plan.Why)
	fmt.Println()
	if plan.Heartbeat < spec.Heartbeat {
		tui.Info("Tighter: Notices A Drop Sooner.")
	} else {
		tui.Info("Looser: A Slow Peer Is Not Declared Dead.")
	}
	fmt.Println()

	if !tui.Confirm("Apply These Timers", false) {
		return
	}
	if err := SetKeepAlive(t.Name, plan); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Timers Applied — Restarted.")
	tui.Warn("Set The Same On The Other Side.")
	fmt.Println()
}

// clientTunnels returns the reverse tunnels that dial out.
//
// A direct tunnel's Iran side also dials out, and is deliberately not included
// here. The screens this feeds — Exit Health, Link Test, the benchmark — do
// not merely read: they rewrite remote_addr, fallback_addrs and
// health_failover through LoadSpec, and those live in [client], which a direct
// config does not have. Widening this test without giving them somewhere to
// write would corrupt the file it was pointed at.
func clientTunnels() []Tunnel {
	var out []Tunnel
	for _, t := range List() {
		if t.Role == "client" && strings.TrimSpace(t.Addr) != "" {
			out = append(out, t)
		}
	}
	return out
}
