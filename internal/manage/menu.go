package manage

import (
	"fmt"

	"github.com/backpack/backpack/internal/tui"
)

// ManageTunnels lists tunnels and lets the user act on a chosen one.
func ManageTunnels() {
	for {
		tunnels := List()
		if len(tunnels) == 0 {
			tui.Warn("No tunnels configured yet.")
			tui.PressEnter()
			return
		}

		tui.Clear()
		tui.Colorize(tui.Cyan, "Manage Tunnels", true)
		fmt.Println()
		labels := make([]string, len(tunnels))
		for i, t := range tunnels {
			state := tui.Color(tui.Red, "stopped")
			if IsActive(t.Service) {
				state = tui.Color(tui.Green, "running")
			}
			labels[i] = fmt.Sprintf("%s  [%s %s]  %s", t.Name, t.Role, t.Transport, state)
		}

		idx := tui.Choose("Select a tunnel to manage:", labels)
		if idx < 0 {
			return
		}
		manageOne(tunnels[idx])
	}
}

// manageOne shows the per-tunnel action menu.
func manageOne(t Tunnel) {
	for {
		tui.Clear()
		state := tui.Color(tui.Red, "stopped")
		if IsActive(t.Service) {
			state = tui.Color(tui.Green, "running")
		}
		tui.Colorize(tui.Cyan, fmt.Sprintf("Tunnel: %s  [%s %s]  %s", t.Name, t.Role, t.Transport, state), true)
		fmt.Println()

		idx := tui.Choose("Choose an action:", []string{
			"Start",
			"Stop",
			"Restart",
			"Live Log",
			"Delete",
		})
		switch idx {
		case 0:
			report(StartService(t.Service), "started")
		case 1:
			report(StopService(t.Service), "stopped")
		case 2:
			report(RestartService(t.Service), "restarted")
		case 3:
			tui.Info("Streaming logs — press Ctrl+C to return.\n")
			FollowLog(t.Service)
		case 4:
			if tui.Confirm(fmt.Sprintf("Delete tunnel %q permanently", t.Name), false) {
				if err := Delete(t.Name); err != nil {
					tui.Error("Delete failed: " + err.Error())
				} else {
					tui.Success("Tunnel deleted.")
				}
				tui.PressEnter()
				return // tunnel no longer exists
			}
		default: // back
			return
		}
	}
}

func report(err error, action string) {
	if err != nil {
		tui.Error(fmt.Sprintf("Action failed: %v", err))
	} else {
		tui.Success("Tunnel " + action + ".")
	}
	tui.PressEnter()
}
