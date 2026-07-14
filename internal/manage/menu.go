package manage

import (
	"fmt"
	"strings"

	"github.com/backpack/backpack/internal/tui"
)

// stateLabel returns a themed running/stopped label for a service.
func stateLabel(service string) string {
	if IsActive(service) {
		return tui.Color(tui.Bold+tui.White, "running")
	}
	return tui.Color(tui.Red, "stopped")
}

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
		opts := make([]tui.Option, len(tunnels))
		for i, t := range tunnels {
			opts[i] = tui.Option{
				Title: t.Name,
				Desc:  fmt.Sprintf("%s %s — %s", t.Role, t.Transport, plainState(t.Service)),
			}
		}

		idx := tui.ChooseOpt("Manage Tunnels — select a tunnel:", opts)
		if idx < 0 {
			return
		}
		manageOne(tunnels[idx])
	}
}

// plainState returns "running"/"stopped" without colors (for gray descriptions).
func plainState(service string) string {
	if IsActive(service) {
		return "running"
	}
	return "stopped"
}

// manageOne shows the per-tunnel action menu.
func manageOne(t Tunnel) {
	for {
		tui.Clear()
		tui.Title(fmt.Sprintf("Tunnel: %s", t.Name))
		fmt.Printf("  %s%s %s%s  %s\n\n", tui.Gray, t.Role, t.Transport, tui.Reset, stateLabel(t.Service))

		idx := tui.ChooseOpt("Choose an action:", []tui.Option{
			{Title: "Edit", Desc: "change tunnel port & forwarded ports"},
			{Title: "Start", Desc: "start the tunnel service"},
			{Title: "Stop", Desc: "stop the tunnel service"},
			{Title: "Restart", Desc: "restart the tunnel service"},
			{Title: "Live Log", Desc: "stream the journal — Ctrl+C to return"},
			{Title: "Delete", Desc: "remove the tunnel permanently"},
		})
		switch idx {
		case 0:
			editPortsMenu(t.Name)
		case 1:
			report(StartService(t.Service), "started")
		case 2:
			report(StopService(t.Service), "stopped")
		case 3:
			report(RestartService(t.Service), "restarted")
		case 4:
			tui.Info("Streaming logs — press Ctrl+C to return.\n")
			FollowLog(t.Service)
		case 5:
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

// editPortsMenu lets the user change a tunnel's ports: the tunnel (control)
// port on both roles, the forwarded ports on servers, and the server address
// on clients. Every change rewrites the config and restarts the tunnel.
func editPortsMenu(name string) {
	for {
		spec, err := LoadSpec(name)
		if err != nil {
			tui.Error("Cannot read tunnel config: " + err.Error())
			tui.PressEnter()
			return
		}

		tui.Clear()
		tui.Title("Edit — " + name)
		fmt.Println()

		if spec.Role == "server" {
			tui.Info("Tunnel (control) port : " + addrPort(spec.BindAddr))
			tui.Info("Forwarded ports       : " + strings.Join(VisiblePorts(spec.Ports), ", "))
			fmt.Println()
			idx := tui.ChooseOpt("Choose:", []tui.Option{
				{Title: "Change tunnel port", Desc: "the control-channel port clients dial"},
				{Title: "Change forwarded ports", Desc: "the ports exposed to users"},
			})
			switch idx {
			case 0:
				changeTunnelPort(name, spec)
			case 1:
				changeForwardedPorts(name, spec)
			default:
				return
			}
		} else {
			tui.Info("Server address : " + spec.RemoteAddr)
			fmt.Println()
			idx := tui.ChooseOpt("Choose:", []tui.Option{
				{Title: "Change server tunnel port", Desc: "must match the server side"},
				{Title: "Change server address", Desc: "IP or domain of the Iran server"},
			})
			switch idx {
			case 0:
				changeTunnelPort(name, spec)
			case 1:
				changeClientHost(name, spec)
			default:
				return
			}
		}
	}
}

// changeTunnelPort prompts for and applies a new tunnel (control) port.
func changeTunnelPort(name string, spec TunnelSpec) {
	cur := addrPort(spec.BindAddr)
	if spec.Role == "client" {
		cur = addrPort(spec.RemoteAddr)
	}
	fmt.Println()
	port := tui.PromptDefault("New tunnel port", cur)
	if port == cur {
		return
	}
	if !validPort(port) {
		tui.Error("Invalid port.")
		tui.PressEnter()
		return
	}
	if spec.Role == "server" && spec.Transport != "udp" && PortInUse(port) {
		tui.Error(fmt.Sprintf("Port %s is already in use on this machine.", port))
		tui.PressEnter()
		return
	}
	if err := EditTunnel(name, "", port, nil); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success(fmt.Sprintf("Tunnel port changed to %s and the tunnel was restarted.", port))
	if spec.Role == "server" {
		tui.Warn("Update the CLIENT side to the same port, or it will not reconnect.")
	}
	tui.PressEnter()
}

// changeForwardedPorts prompts for and applies a new forwarded-ports list.
func changeForwardedPorts(name string, spec TunnelSpec) {
	fmt.Println()
	tui.Info("Current: " + strings.Join(VisiblePorts(spec.Ports), ", "))
	tui.Warn("Enter the FULL new list (comma separated, e.g. 443,8080 or 443=1.1.1.1:443).")
	raw := tui.Prompt("New forwarded ports: ")
	ports := parsePorts(raw)
	if len(ports) == 0 {
		tui.Error("No valid ports entered.")
		tui.PressEnter()
		return
	}
	if err := EditTunnel(name, "", "", ports); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Forwarded ports updated and the tunnel was restarted.")
	tui.PressEnter()
}

// changeClientHost prompts for and applies a new server address on a client.
func changeClientHost(name string, spec TunnelSpec) {
	fmt.Println()
	host := tui.PromptDefault("New server address (IP or domain)", addrHost(spec.RemoteAddr, ""))
	if strings.TrimSpace(host) == "" {
		tui.Error("Address cannot be empty.")
		tui.PressEnter()
		return
	}
	if err := EditTunnel(name, host, "", nil); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Server address updated and the tunnel was restarted.")
	tui.PressEnter()
}
