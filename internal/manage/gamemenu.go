package manage

import (
	"fmt"

	"github.com/backpack/backpack/internal/tui"
)

// GameLatencyTest estimates the in-game ping a player would feel through this
// server, without anyone having to install a game or a client config. It is
// meant to run on the abroad (kharej) exit — the side nearest the games — where
// the exit-to-game leg it measures is the leg that actually varies.
func GameLatencyTest() {
	for {
		tui.Clear()
		tui.Title("Game Latency Test")
		idx := tui.ChooseOpt("Game Latency Test", []tui.Option{
			{Title: "Test one game", Desc: "pick a game and see the estimated in-game ping"},
			{Title: "Test all games", Desc: "ping every bundled endpoint through this exit"},
			{Title: "Test a custom host", Desc: "an address you know your own traffic uses"},
			{Title: "Where the endpoint list lives", Desc: "correct the game addresses by hand"},
		})
		switch idx {
		case 0:
			gameTestOne()
		case 1:
			gameTestAll()
		case 2:
			gameTestCustom()
		case 3:
			gameListLocation()
		default:
			return
		}
	}
}

// pickExitTunnel returns the tunnel whose hub leg to add to a game measurement.
// The dial-out (client) side is the abroad exit, and its address is the hub it
// tunnels back to, so probing that address gives the hub-to-exit leg. Returns
// ok=false when there is no such tunnel, in which case only the exit-to-game leg
// is known.
func pickExitTunnel() (Tunnel, bool) {
	clients := clientTunnels()
	switch len(clients) {
	case 0:
		return Tunnel{}, false
	case 1:
		return clients[0], true
	default:
		opts := make([]tui.Option, len(clients))
		for i, t := range clients {
			opts[i] = tui.Option{Title: t.Name, Desc: t.Addr + " — " + transportLabel(t.Transport)}
		}
		idx := tui.ChooseOpt("Estimate through which tunnel?", opts)
		if idx < 0 {
			return Tunnel{}, false
		}
		return clients[idx], true
	}
}

// tunnelLegMS measures the hub-to-exit round trip in milliseconds. It reuses the
// TCP path probe, so it only works on a TCP-based tunnel; a datagram tunnel's
// port cannot be probed this way, and the leg is reported as unknown.
func tunnelLegMS(t Tunnel) (float64, bool) {
	if isDatagram(t.Transport) {
		return 0, false
	}
	q := ProbePath(t.Addr)
	if !q.Usable() {
		return 0, false
	}
	return float64(q.Avg.Microseconds()) / 1000, true
}

func gameTestOne() {
	eps := LoadGameEndpoints()
	if len(eps) == 0 {
		tui.Error("The endpoint list is empty.")
		tui.PressEnter()
		return
	}
	opts := make([]tui.Option, len(eps))
	for i, e := range eps {
		opts[i] = tui.Option{Title: e.Game, Desc: e.Region + " — " + e.Note}
	}
	idx := tui.ChooseOpt("Which game?", opts)
	if idx < 0 {
		return
	}
	ep := eps[idx]

	exitT, haveTunnel := pickExitTunnel()

	tui.Clear()
	tui.Title(ep.Game + " via " + ep.Region)
	fmt.Println()
	tui.Info("Measuring — this takes a few seconds...")
	fmt.Println()

	g := pingHost(ep.Host, 25)
	reportGameLeg(ep.Host, g, exitT, haveTunnel)
	tui.PressEnter()
}

func gameTestAll() {
	eps := LoadGameEndpoints()
	if len(eps) == 0 {
		tui.Error("The endpoint list is empty.")
		tui.PressEnter()
		return
	}
	exitT, haveTunnel := pickExitTunnel()
	var tunnelMS float64
	if haveTunnel {
		tunnelMS, haveTunnel = tunnelLegMS(exitT)
	}

	tui.Clear()
	tui.Title("Every game through this exit")
	fmt.Println()
	if haveTunnel {
		tui.Info(fmt.Sprintf("Hub-to-exit leg (via %s): %.0fms — added to every estimate below.", exitT.Name, tunnelMS))
	} else {
		tui.Warn("No measurable hub-to-exit leg, so these are the exit-to-game legs only.")
		tui.Warn("Add your own tunnel round trip (Link Test) to read them as player ping.")
	}
	fmt.Println()
	tui.Info(fmt.Sprintf("  %-22s %-13s %8s %7s   %s", "GAME", "REGION", "EXIT>SRV", "LOSS", "EST PING"))

	for _, ep := range eps {
		g := pingHost(ep.Host, 10)
		if !g.OK {
			tui.Warn(fmt.Sprintf("  %-22s %-13s %8s %7s   %s", ep.Game, ep.Region, "no reply", "-", "filtered"))
			continue
		}
		// "Playable" here folds in a typical ADSL last mile so the column is a
		// real player number rather than a server-only one.
		est := int(tunnelMS + g.AvgMS + 25)
		r := RateEstimatedPing(est)
		line := fmt.Sprintf("  %-22s %-13s %6.0fms %6.1f%%   %4dms %s",
			ep.Game, ep.Region, g.AvgMS, g.LossPct, est, r.Label)
		printBySeverity(r.Severity, line)
	}
	fmt.Println()
	tui.Warn("Many game servers filter ICMP — \"no reply\" means this endpoint does not")
	tui.Warn("answer ping, not that the route is broken. Try another region, or a")
	tui.Warn("custom host you have seen your own traffic use.")
	tui.PressEnter()
}

func gameTestCustom() {
	host := tui.Prompt("Host or IP to test")
	if host == "" {
		return
	}
	exitT, haveTunnel := pickExitTunnel()

	tui.Clear()
	tui.Title("Custom host: " + host)
	fmt.Println()
	tui.Info("Measuring...")
	fmt.Println()
	g := pingHost(host, 25)
	reportGameLeg(host, g, exitT, haveTunnel)
	tui.PressEnter()
}

// reportGameLeg prints one host's result in detail: the exit-to-game leg, the
// hub-to-exit leg when it is known, their total, and what a real player would
// feel once their own last mile is added.
func reportGameLeg(host string, g GamePing, exitT Tunnel, haveTunnel bool) {
	if !g.OK {
		tui.Warn("No ICMP reply from " + host + ".")
		tui.Warn("Most game servers filter ping. This does NOT mean the route is broken —")
		tui.Warn("it means this endpoint will not answer. Try another region or host.")
		fmt.Println()
		return
	}

	var tunnelMS float64
	if haveTunnel {
		tunnelMS, haveTunnel = tunnelLegMS(exitT)
	}

	tui.Info(fmt.Sprintf("  exit → game server     %.0fms   loss %.1f%%", g.AvgMS, g.LossPct))
	if haveTunnel {
		tui.Info(fmt.Sprintf("  hub → exit (tunnel)    %.0fms   via %s", tunnelMS, exitT.Name))
	} else {
		tui.Warn("  hub → exit (tunnel)    unknown — run Link Test on a TCP tunnel to measure it")
	}
	serverSide := tunnelMS + g.AvgMS
	fmt.Println()
	tui.Info(fmt.Sprintf("  server-side total      %.0fms   everything you control", serverSide))
	fmt.Println()

	tui.Title("What the player will see")
	tui.Warn("Their own latency to the hub is added on top. Typical:")
	fmt.Println()
	for _, pl := range playerLatencies {
		est := int(serverSide) + pl.MS
		r := RateEstimatedPing(est)
		printBySeverity(r.Severity, fmt.Sprintf("    %-18s +%-4dms = %4dms  %s", pl.Where, pl.MS, est, r.Label))
	}
	fmt.Println()
	if g.LossPct > 2 {
		tui.Error(fmt.Sprintf("  %.1f%% loss on the exit-to-game leg.", g.LossPct))
		tui.Warn("  That is beyond the tunnel — the exit datacentre's route to the game.")
		tui.Warn("  FEC does not cover this leg; another exit region may be better.")
		fmt.Println()
	}
}

func gameListLocation() {
	LoadGameEndpoints() // seed the file if it does not exist yet
	tui.Clear()
	tui.Title("Game endpoint list")
	fmt.Println()
	tui.Info("The addresses live in:")
	tui.Success("  " + gameListFile)
	fmt.Println()
	tui.Info("One endpoint per line:  game|region|host|note")
	tui.Warn("The bundled addresses are best-effort — publishers move them without")
	tui.Warn("notice. Replace them with hosts you have verified for numbers you can")
	tui.Warn("rely on. Lines starting with # are ignored.")
	fmt.Println()
	tui.PressEnter()
}

// printBySeverity colours a line the way the game ratings read: green for good,
// the muted warn colour for marginal, red for bad.
func printBySeverity(sev int, line string) {
	switch sev {
	case 0:
		tui.Success(line)
	case 2:
		tui.Error(line)
	default:
		tui.Warn(line)
	}
}
