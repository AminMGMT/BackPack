package manage

import (
	"fmt"

	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/utils/network"
)

// ExitHealth scores every server address a client tunnel can use and ranks them,
// so an operator can see which exit is healthiest right now and pin it. It is
// the manual companion to automatic failover: the same score, read by a person
// instead of a steering loop.
func ExitHealth() {
	tui.Clear()
	tui.Title("Exit Health")
	tui.Warn("Scores every configured server address by latency, jitter and loss")
	tui.Warn("(score = rtt + 2·jitter + 20·loss%, lower is better) and ranks them.")
	fmt.Println()

	// Only tunnels with a backup address have anything to compare.
	var cands []Tunnel
	for _, t := range clientTunnels() {
		if s, err := LoadSpec(t.Name); err == nil && len(s.FallbackAddrs) > 0 {
			cands = append(cands, t)
		}
	}
	if len(cands) == 0 {
		tui.Info("No client tunnel has backup addresses to compare.")
		tui.Warn("Add fallback server addresses when creating or editing a tunnel to")
		tui.Warn("use multi-exit failover.")
		tui.PressEnter()
		return
	}

	var target Tunnel
	if len(cands) == 1 {
		target = cands[0]
	} else {
		opts := make([]tui.Option, len(cands))
		for i, t := range cands {
			opts[i] = tui.Option{Title: t.Name, Desc: t.Addr + " — " + transportLabel(t.Transport)}
		}
		idx := tui.ChooseOpt("Which tunnel's exits should be scored?", opts)
		if idx < 0 {
			return
		}
		target = cands[idx]
	}

	spec, err := LoadSpec(target.Name)
	if err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	addrs := append([]string{spec.RemoteAddr}, spec.FallbackAddrs...)

	fmt.Println()
	tui.Info(fmt.Sprintf("Measuring %d exits — a few seconds...", len(addrs)))
	fmt.Println()
	scores := network.ScoreEndpoints(addrs, 10)

	tui.Title("Ranked best first")
	fmt.Println()
	tui.Info(fmt.Sprintf("  %-24s %8s %8s %7s %8s", "EXIT", "RTT", "JITTER", "LOSS", "SCORE"))
	for _, s := range scores {
		tag := ""
		if s.Addr == spec.RemoteAddr {
			tag = " (current primary)"
		}
		if !s.Reachable {
			tui.Warn(fmt.Sprintf("  %-24s %8s %8s %7s %8s%s", s.Addr, "no reply", "-", "-", "-", tag))
			continue
		}
		line := fmt.Sprintf("  %-24s %6.0fms %6.1fms %6.1f%% %8.0f%s",
			s.Addr, s.RTTms, s.JitterMs, s.LossPct, s.Score, tag)
		if s.LossPct >= 2 || s.JitterMs >= 30 {
			tui.Warn(line)
		} else {
			tui.Success(line)
		}
	}
	fmt.Println()
	tui.Warn("Scoring pings each address, so an exit that filters ICMP shows as")
	tui.Warn("\"no reply\" even when the tunnel through it is fine.")
	fmt.Println()

	best := scores[0]
	if !best.Reachable || best.Addr == spec.RemoteAddr {
		if best.Addr == spec.RemoteAddr && best.Reachable {
			tui.Success("The current primary is already the healthiest exit — nothing to change.")
		}
		if spec.HealthFailover {
			tui.Info("Automatic failover is on, so traffic already tracks the best exit on its own.")
		}
		tui.PressEnter()
		return
	}

	tui.Info(fmt.Sprintf("Healthiest exit: %s (score %.0f), currently a backup.", best.Addr, best.Score))
	if !tui.Confirm("Pin "+best.Addr+" as the primary exit", false) {
		tui.PressEnter()
		return
	}
	if err := pinPrimaryExit(target.Name, best.Addr); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Pinned " + best.Addr + " as the primary and restarted the tunnel.")
	if !spec.HealthFailover {
		tui.Info("Tip: turn on automatic failover so this tracks the best exit for you.")
	}
	fmt.Println()
	tui.PressEnter()
}

// pinPrimaryExit makes addr the tunnel's primary server address, demoting the
// old primary into the fallback list so nothing is lost. The other fallbacks
// keep their order.
func pinPrimaryExit(name, addr string) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	if addr == s.RemoteAddr {
		return fmt.Errorf("that address is already the primary")
	}
	s.RemoteAddr, s.FallbackAddrs = reorderPrimary(s.RemoteAddr, s.FallbackAddrs, addr)
	return applySpec(s)
}

// reorderPrimary promotes newPrimary to the front, demotes the old primary into
// the backups, and drops newPrimary's old backup slot — so no address is lost or
// duplicated and the remaining order is preserved.
func reorderPrimary(oldPrimary string, fallbacks []string, newPrimary string) (string, []string) {
	rest := make([]string, 0, len(fallbacks)+1)
	rest = append(rest, oldPrimary)
	for _, f := range fallbacks {
		if f != newPrimary {
			rest = append(rest, f)
		}
	}
	return newPrimary, rest
}
