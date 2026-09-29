package manage

import (
	"fmt"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/sysstat"
	"github.com/backpack/backpack/internal/tui"
)

// TunnelMetrics shows what each tunnel has actually carried.
//
// Unlike the Link Test, which probes the path, these numbers come from the
// traffic the tunnel really moved — so on KCP they answer the question that
// matters most: is error correction repairing anything, or is this link clean
// enough that KCP is only costing you parity packets?
func TunnelMetrics() {
	tui.Clear()
	tui.Title("Tunnel Metrics")
	fmt.Println()

	tunnels := List()
	if len(tunnels) == 0 {
		tui.Warn("No Tunnels Yet.")
		tui.PressEnter()
		return
	}

	shown := 0
	for _, t := range tunnels {
		snap, err := metrics.Read(app.ConfigDir, t.Name)
		if err != nil {
			tui.Info(tui.Color(tui.Bold+tui.White, t.Name))
			if IsActive(t.Service) {
				tui.Warn("  No Readings Yet (Within 30s Of Starting)")
			} else {
				tui.Warn("  Not Running")
			}
			fmt.Println()
			continue
		}
		shown++
		printSnapshot(t, snap)
	}

	if shown == 0 {
		tui.Warn("Nothing Recorded Yet.")
	}
	tui.PressEnter()
}

// printSnapshot renders one tunnel's counters.
func printSnapshot(t Tunnel, s metrics.Snapshot) {
	tui.Info(tui.Color(tui.Bold+tui.White, t.Name) +
		tui.Color(tui.Gray, fmt.Sprintf("  %s / %s", s.Role, transportLabel(s.Transport))))

	age := time.Since(s.Taken).Round(time.Second)
	tui.Warn(fmt.Sprintf("  Recorded %s Ago, Up %s", age, s.Uptime))
	tui.Info(fmt.Sprintf("  Traffic       : %s In, %s Out",
		sysstat.HumanBytes(s.BytesIn), sysstat.HumanBytes(s.BytesOut)))

	if s.KCP == nil {
		fmt.Println()
		return
	}

	k := s.KCP
	tui.Info(fmt.Sprintf("  Packets       : %d In, %d Out", k.PacketsIn, k.PacketsOut))

	lossLine := fmt.Sprintf("  Link quality  : %.2f%% of packets needed repair", k.LossPercent())
	switch {
	case k.LossPercent() >= 5:
		tui.Error(lossLine)
	case k.LossPercent() >= 1:
		tui.Warn(lossLine)
	default:
		tui.Info(lossLine)
	}
	tui.Warn(fmt.Sprintf("      Resent %d, Lost %d, Duplicated %d",
		k.Retransmitted, k.Lost, k.Duplicated))

	// The headline number: packets rebuilt from parity never had to be waited
	// for, which is the entire reason to run KCP instead of TCP Mux.
	if k.FECRecovered > 0 {
		tui.Success(fmt.Sprintf("  FEC           : %d Packets Rebuilt",
			k.FECRecovered))
		if k.FECErrors > 0 {
			tui.Warn(fmt.Sprintf("      %d Groups Too Damaged", k.FECErrors))
		}
	} else if k.PacketsIn > 0 {
		tui.Info("  FEC           : Nothing Rebuilt — Clean Link")
		tui.Warn("      TCP Mux Would Be Lighter Here")
	}
	fmt.Println()
}

// humanBytes renders a byte count the way a person reads it.
