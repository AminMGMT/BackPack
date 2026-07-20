package manage

import (
	"fmt"
	"strings"

	"github.com/backpack/backpack/internal/app"
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
			tui.Info("Forwarded ports       : " + strings.Join(VisiblePorts(spec.Ports, spec.Token), ", "))
			tui.Info("Transport             : " + transportLabel(spec.Transport))
			tui.Info("Performance preset    : " + presetLabel(spec.Preset))
			if supportsProxyProtocol(spec.Transport) {
				tui.Info("Real client IP        : " + onOff(spec.ProxyProtocol))
			}
			tui.Info("Limits                : " + limitsSummary(spec))
			if needsTLS(spec.Transport) {
				tui.Info("Certificate           : " + certSummary(spec))
			}
			fmt.Println()
			opts := []tui.Option{
				{Title: "Change tunnel port", Desc: "the control-channel port clients dial"},
				{Title: "Change forwarded ports", Desc: "the ports exposed to users"},
				{Title: "Change transport", Desc: "switch carrier — keeps the token and ports"},
				{Title: "Change performance preset", Desc: "Balance, Turbo or Aggressive"},
				{Title: "Real client IP", Desc: "send the user's real IP so panels can limit devices"},
				{Title: "Limits", Desc: "cap connections and bandwidth for this tunnel"},
			}
			if needsTLS(spec.Transport) {
				opts = append(opts, tui.Option{
					Title: "Certificate",
					Desc:  "self-signed, or a real one from Let's Encrypt (needs a domain)",
				})
			}
			idx := tui.ChooseOpt("Choose:", opts)
			switch idx {
			case 0:
				changeTunnelPort(name, spec)
			case 1:
				changeForwardedPorts(name, spec)
			case 2:
				changeTunnelTransport(name, spec)
			case 3:
				changeTunnelPreset(name, spec)
			case 4:
				toggleProxyProtocol(name, spec)
			case 5:
				editLimits(name, spec)
			case 6:
				if needsTLS(spec.Transport) {
					editCertificate(name, spec)
				}
			default:
				return
			}
		} else {
			tui.Info("Server address : " + spec.RemoteAddr)
			tui.Info("Transport      : " + transportLabel(spec.Transport))
			tui.Info("Backup servers : " + fallbackSummary(spec.FallbackAddrs))
			tui.Info("Preset         : " + presetLabel(spec.Preset))
			tui.Info("Load balancing : " + onOff(spec.LoadBalance))
			fmt.Println()
			idx := tui.ChooseOpt("Choose:", []tui.Option{
				{Title: "Change server tunnel port", Desc: "must match the server side"},
				{Title: "Change server address", Desc: "IP or domain of the Iran server"},
				{Title: "Change transport", Desc: "switch carrier — keeps the token"},
				{Title: "Backup server addresses", Desc: "auto-failover when the main IP gets blocked"},
				{Title: "Change performance preset", Desc: "Balance, Turbo or Aggressive"},
				{Title: "Load balancing", Desc: "use all backup addresses at once, not just as spares"},
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
			case 4:
				changeTunnelPreset(name, spec)
			case 5:
				toggleLoadBalance(name, spec)
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
	// Check the protocol the transport actually binds: a UDP-based tunnel is
	// unaffected by whatever holds the same TCP port, and vice versa.
	if spec.Role == "server" && TunnelPortInUse(spec.Transport, port) {
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
	tui.Info("Current: " + strings.Join(VisiblePorts(spec.Ports, spec.Token), ", "))
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

// onOff renders a boolean the way the rest of the menus read.
func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// toggleLoadBalance switches balancing across the backup addresses on or off.
func toggleLoadBalance(name string, spec TunnelSpec) {
	fmt.Println()
	tui.Title("Load balancing")
	tui.Warn("Off, the backup addresses are spares: the tunnel uses one at a time")
	tui.Warn("and only moves when that one stops answering.")
	tui.Warn("On, the tunnel's connections are spread over all of them at once, so")
	tui.Warn("one throttled route only slows its own share of the traffic.")
	fmt.Println()
	tui.Error("Every address must reach the SAME server — a second IP of it, another")
	tui.Error("of its ports, or a CDN edge in front of it. Addresses that lead to a")
	tui.Error("different machine will not work: only one of them has your tunnel.")
	fmt.Println()
	tui.Info("Backup addresses : " + fallbackSummary(spec.FallbackAddrs))
	tui.Info("Currently        : " + onOff(spec.LoadBalance))
	fmt.Println()

	want := !spec.LoadBalance
	verb := "Enable"
	if !want {
		verb = "Disable"
	}
	if !tui.Confirm(verb+" load balancing for "+name, false) {
		return
	}
	if err := SetLoadBalance(name, want); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Load balancing is now " + onOff(want) + " and the tunnel restarted.")
	tui.PressEnter()
}

// limitsSummary renders the configured caps for the Edit header.
func limitsSummary(spec TunnelSpec) string {
	switch {
	case spec.MaxConnections == 0 && spec.BandwidthMbps == 0:
		return "none"
	case spec.BandwidthMbps == 0:
		return fmt.Sprintf("%d connections", spec.MaxConnections)
	case spec.MaxConnections == 0:
		return fmt.Sprintf("%d Mbit/s", spec.BandwidthMbps)
	default:
		return fmt.Sprintf("%d connections, %d Mbit/s", spec.MaxConnections, spec.BandwidthMbps)
	}
}

// editLimits sets the per-tunnel connection and bandwidth caps.
func editLimits(name string, spec TunnelSpec) {
	fmt.Println()
	tui.Title("Limits for " + name)
	fmt.Println()
	tui.Warn("Caps for this tunnel as a whole — useful when several services or")
	tui.Warn("customers share one link and you do not want any of them able to")
	tui.Warn("take all of it.")
	tui.Warn("Enter 0 for no limit. Both are 0 by default.")
	fmt.Println()

	maxConns := tui.PromptInt("Maximum simultaneous connections", spec.MaxConnections)
	bandwidth := tui.PromptInt("Bandwidth limit in Mbit/s", spec.BandwidthMbps)

	if maxConns == spec.MaxConnections && bandwidth == spec.BandwidthMbps {
		tui.Info("Nothing changed.")
		tui.PressEnter()
		return
	}
	if maxConns > 0 && maxConns < 10 {
		tui.Warn(fmt.Sprintf("%d is a very low connection cap — a single browser can open more than that.", maxConns))
		if !tui.Confirm("Use it anyway", false) {
			return
		}
	}
	if err := SetLimits(name, maxConns, bandwidth); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Limits updated and the tunnel restarted.")
	tui.PressEnter()
}

// toggleProxyProtocol switches forwarding of the real client IP on or off.
func toggleProxyProtocol(name string, spec TunnelSpec) {
	fmt.Println()
	tui.Title("Real client IP (PROXY protocol)")
	fmt.Println()
	tui.Warn("Off, the service behind the tunnel sees every connection as coming")
	tui.Warn("from the tunnel itself — so a panel counts all your users as one")
	tui.Warn("device and per-user device limits cannot work.")
	tui.Warn("On, each connection is prefixed with a PROXY protocol v2 header")
	tui.Warn("carrying the user's real IP and port.")
	fmt.Println()
	tui.Error("The service MUST be configured to accept the PROXY protocol first.")
	tui.Error("If it is not, it reads the header as traffic and every connection")
	tui.Error("breaks — so turn it on there before turning it on here.")
	tui.Warn("In X-UI / Marzban it is the inbound option \"Accept Proxy Protocol\".")
	fmt.Println()
	tui.Info("Currently : " + onOff(spec.ProxyProtocol))
	fmt.Println()

	want := !spec.ProxyProtocol
	verb := "Enable"
	if !want {
		verb = "Disable"
	}
	if !tui.Confirm(verb+" the real client IP header for "+name, false) {
		return
	}
	if err := SetProxyProtocol(name, want); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Real client IP is now " + onOff(want) + " and the tunnel restarted.")
	tui.PressEnter()
}

// changeTunnelPreset re-applies a whole performance profile to a tunnel.
func changeTunnelPreset(name string, spec TunnelSpec) {
	fmt.Println()
	tui.Info("Current preset: " + presetLabel(spec.Preset))
	tui.Warn("A preset rewrites every tuning value — buffers, pool size, mux windows")
	tui.Warn("and, on KCP, the retransmission and error-correction settings.")
	tui.Warn("Use the SAME preset on both sides so the two ends stay matched.")
	fmt.Println()

	newPreset := choosePreset()
	if newPreset == spec.Preset {
		tui.Info("That is already the current preset.")
		tui.PressEnter()
		return
	}
	if !tui.Confirm(fmt.Sprintf("Apply the %s preset to %q now", presetLabel(newPreset), name), true) {
		return
	}

	if err := ChangePreset(name, newPreset); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Preset changed to " + presetLabel(newPreset) + " and the tunnel restarted.")
	tui.Warn("Apply the same preset on the OTHER side too.")
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

// certSummary describes which certificate a TLS tunnel is using.
func certSummary(s TunnelSpec) string {
	if s.ACMEDomain != "" {
		return "Let's Encrypt (" + s.ACMEDomain + ")"
	}
	return "self-signed"
}

// editCertificate switches a wss/wssmux tunnel between the generated
// self-signed certificate and a real one from Let's Encrypt.
func editCertificate(name string, s TunnelSpec) {
	tui.Clear()
	tui.Title("Certificate for " + name)
	fmt.Println()
	tui.Info("Current: " + certSummary(s))
	fmt.Println()
	tui.Warn("A self-signed certificate encrypts exactly as well — the client is")
	tui.Warn("Backpack's own code and does not verify it. The reason to use a real")
	tui.Warn("one is how the connection looks from outside: real HTTPS on port 443")
	tui.Warn("is never self-signed, so a self-signed certificate is a distinguishing")
	tui.Warn("mark. A real one removes it, and a CDN in front of the tunnel needs it.")
	fmt.Println()

	idx := tui.ChooseOpt("Use:", []tui.Option{
		{Title: "Self-signed", Desc: "works anywhere, including on a bare IP — the default"},
		{Title: "Let's Encrypt", Desc: "needs a domain pointing at THIS server"},
	})

	switch idx {
	case 0:
		if s.ACMEDomain == "" {
			tui.Info("Already using the self-signed certificate.")
			tui.PressEnter()
			return
		}
		s.ACMEDomain, s.ACMEEmail = "", ""

	case 1:
		domain, email, ok := promptACMEDomain(s.ACMEDomain, s.ACMEEmail)
		if !ok {
			return
		}
		s.ACMEDomain, s.ACMEEmail = domain, email

	default:
		return
	}

	if err := SetCertificate(name, s.ACMEDomain, s.ACMEEmail); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}

	tui.Success("Certificate settings saved and the tunnel restarted.")
	if s.ACMEDomain != "" {
		fmt.Println()
		tui.Warn("The certificate is requested on the first connection, which can")
		tui.Warn("take a few seconds. If it does not appear, check the log:")
		tui.Warn("  journalctl -u " + app.ServiceName(name) + " -n 50")
	}
	tui.PressEnter()
}

// contains reports whether list holds v.
func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
