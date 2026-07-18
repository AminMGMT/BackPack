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
			tui.Info("Transport             : " + transportLabel(spec.Transport))
			fmt.Println()
			idx := tui.ChooseOpt("Choose:", []tui.Option{
				{Title: "Change tunnel port", Desc: "the control-channel port clients dial"},
				{Title: "Change forwarded ports", Desc: "the ports exposed to users"},
				{Title: "Change transport", Desc: "switch carrier — keeps the token and ports"},
			})
			switch idx {
			case 0:
				changeTunnelPort(name, spec)
			case 1:
				changeForwardedPorts(name, spec)
			case 2:
				changeTunnelTransport(name, spec)
			default:
				return
			}
		} else {
			tui.Info("Server address : " + spec.RemoteAddr)
			tui.Info("Transport      : " + transportLabel(spec.Transport))
			tui.Info("Backup servers : " + fallbackSummary(spec.FallbackAddrs))
			fmt.Println()
			idx := tui.ChooseOpt("Choose:", []tui.Option{
				{Title: "Change server tunnel port", Desc: "must match the server side"},
				{Title: "Change server address", Desc: "IP or domain of the Iran server"},
				{Title: "Change transport", Desc: "switch carrier — keeps the token"},
				{Title: "Backup server addresses", Desc: "auto-failover when the main IP gets blocked"},
			})
			switch idx {
			case 0:
				changeTunnelPort(name, spec)
			case 1:
				changeClientHost(name, spec)
			case 2:
				changeTunnelTransport(name, spec)
			case 3:
				changeFallbackAddrs(name, spec)
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

// fallbackSummary renders the backup-address list for the Edit header.
func fallbackSummary(addrs []string) string {
	if len(addrs) == 0 {
		return "none"
	}
	return strings.Join(addrs, ", ")
}

// changeTunnelTransport switches the tunnel's carrier, keeping its name, token
// and ports. Both ends must match, so the user is reminded to switch the peer.
func changeTunnelTransport(name string, spec TunnelSpec) {
	fmt.Println()
	tui.Info("Current transport: " + transportLabel(spec.Transport))
	tui.Warn("The other side must use the SAME transport, so switch it there too.")
	fmt.Println()

	newTransport := chooseTransport()
	if newTransport == "" {
		return
	}
	if newTransport == spec.Transport {
		tui.Info("That is already the current transport.")
		tui.PressEnter()
		return
	}
	if spec.Role == "server" && needsTLS(newTransport) {
		tui.Info("A self-signed TLS certificate will be generated automatically if needed.")
	}
	if !tui.Confirm(fmt.Sprintf("Switch %q to %s now", name, transportLabel(newTransport)), true) {
		return
	}

	if err := ChangeTransport(name, newTransport); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Transport switched to " + transportLabel(newTransport) + " and the tunnel restarted.")
	tui.Warn("Now switch the OTHER side to the same transport, or it cannot reconnect.")
	tui.PressEnter()
}

// changeFallbackAddrs manages the client's backup server addresses. This is what
// keeps a tunnel alive when the main server IP is filtered: the client walks the
// list until one address answers.
func changeFallbackAddrs(name string, spec TunnelSpec) {
	fmt.Println()
	tui.Title("Backup server addresses")
	tui.Warn("If the main server address stops answering (a filtered IP, a blocked")
	tui.Warn("port, or a CDN edge you want to use), the client automatically tries")
	tui.Warn("these in order until one connects — no manual switching needed.")
	fmt.Println()
	tui.Info("Main address : " + spec.RemoteAddr)
	tui.Info("Backups now  : " + fallbackSummary(spec.FallbackAddrs))
	fmt.Println()
	tui.Warn("Enter the FULL new list, comma separated. A bare IP/host reuses the")
	tui.Warn("main port, e.g.:  1.2.3.4, 5.6.7.8:8443, edge.example.com:443")
	tui.Warn("Leave empty to remove all backups.")
	fmt.Println()

	raw := tui.Prompt("Backup addresses: ")
	var addrs []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			addrs = append(addrs, p)
		}
	}

	if err := SetFallbackAddrs(name, addrs); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	if len(addrs) == 0 {
		tui.Success("Backup addresses cleared — the tunnel restarted.")
	} else {
		tui.Success(fmt.Sprintf("%d backup address(es) saved — the tunnel restarted.", len(addrs)))
		tui.Info("The client will fail over automatically if the main address stops answering.")
	}
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
