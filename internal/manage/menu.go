package manage

import (
	"fmt"
	"net"
	"strings"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/utils/network"
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
			tui.Warn("No Tunnels Yet.")
			tui.PressEnter()
			return
		}

		tui.Clear()
		opts := make([]tui.Option, len(tunnels))
		for i, t := range tunnels {
			transport := t.Transport
			if spec, err := LoadSpec(t.Name); err == nil {
				transport = selectedTransport(spec)
			}
			opts[i] = tui.Option{
				Title: t.Name,
				Desc:  fmt.Sprintf("%s %s — %s", t.Role, transportLabel(transport), plainState(t.Service)),
			}
		}

		idx := tui.ChooseOpt("Select A Tunnel", opts)
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

		// A tunnel whose other end is on a managed server is one tunnel in two
		// places, and this menu can only change one of them: the channel to the
		// node belongs to the running panel, and this is a separate process
		// that has no way to reach it.
		//
		// Editing here is still allowed — the operator may have a reason, and a
		// menu that refuses is a menu people work around — but they are told
		// first, because the failure it causes is silent. Both ends report
		// themselves as running and no traffic passes.
		if on, ok := NodeFor(t.Name); ok {
			tui.Warn(fmt.Sprintf("Other End Is On %q — Edit From The Web Panel So Both Ends Match.\n", on))
		}

		idx := tui.ChooseOpt("Action", []tui.Option{
			{Title: "Edit", Desc: "ports, transport, settings"},
			{Title: "Start", Desc: ""},
			{Title: "Stop", Desc: ""},
			{Title: "Restart", Desc: ""},
			{Title: "Live Log", Desc: "Ctrl+C to return"},
			{Title: "Setup Link", Desc: "builds the other end"},
			{Title: "Delete", Desc: "remove permanently"},
		})
		switch idx {
		case 0:
			// The reverse editor reads [server] and [client], which a direct
			// config does not have. Sending one there would show an empty
			// screen and, worse, could write a reverse-shaped file over it.
			if IsDirectKind(t) {
				editDirectMenu(t)
				break
			}
			editPortsMenu(t.Name)
		case 1:
			report(StartService(t.Service), "Started")
		case 2:
			report(StopService(t.Service), "Stopped")
		case 3:
			report(RestartService(t.Service), "Restarted")
		case 4:
			tui.Info("Live Log — Ctrl+C To Return.\n")
			FollowLog(t.Service)
		case 5:
			showShareLink(t.Name)
		case 6:
			if tui.Confirm(fmt.Sprintf("Delete %q Permanently", t.Name), false) {
				if err := Delete(t.Name); err != nil {
					tui.Error("Delete failed: " + err.Error())
				} else {
					tui.Success("Deleted.")
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
		tui.Error(fmt.Sprintf("Failed: %v", err))
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
			tui.Error("Cannot read the config: " + err.Error())
			tui.PressEnter()
			return
		}

		tui.Clear()
		tui.Title("Edit — " + name)
		fmt.Println()
		if managedTransport(selectedTransport(spec)) {
			if !editManagedTunnelMenu(name, spec) {
				return
			}
			continue
		}

		if spec.Role == "server" {
			// Shown with its address when the control port is pinned to one,
			// because "443" alone would read as every interface.
			shown := addrPort(spec.BindAddr)
			if h := bindHostOf(spec.BindAddr); h != "" {
				shown = net.JoinHostPort(h, shown)
			}
			tui.Info("Tunnel Port     : " + shown)
			tui.Info("Forwarded Ports : " + strings.Join(VisiblePorts(spec.Ports, spec.Token), ", "))
			tui.Info("Transport       : " + transportLabel(spec.Transport))
			tui.Info("Preset          : " + presetLabel(spec.Preset))
			if supportsProxyProtocol(spec.Transport) {
				tui.Info("Real Client IP  : " + onOff(spec.ProxyProtocol))
			}
			tui.Info("Limits          : " + limitsSummary(spec))
			if !isDatagram(spec.Transport) {
				tui.Info("TCP MSS Clamp   : " + mssLabel(spec.MSS))
			}
			if spec.Transport == "pck" {
				tui.Info("TCP Flags       : " + pckFlagSummary(spec.PckFlags))
			}
			if needsTLS(spec.Transport) {
				tui.Info("Certificate     : " + certSummary(spec))
			}
			fmt.Println()
			// Options and handlers are built side by side rather than dispatched
			// through a switch on a fixed index: two of these entries only exist
			// for some transports, and a numbered switch has to be re-counted
			// every time one is added — which is how an entry ends up running the
			// action below it.
			opts := []tui.Option{
				{Title: "Tunnel Port", Desc: "the port the kharej dials"},
				{Title: "Forwarded Ports", Desc: "the ports users connect to"},
				{Title: "Transport", Desc: "keeps the token and ports"},
				{Title: "Preset", Desc: "Balance, Turbo or Aggressive"},
				{Title: "Real Client IP", Desc: "PROXY protocol, for device limits"},
				{Title: "Limits", Desc: "connections and bandwidth"},
			}
			actions := []func(){
				func() { changeTunnelPort(name, spec) },
				func() { changeForwardedPorts(name, spec) },
				func() { changeTunnelTransport(name, spec) },
				func() { changeTunnelPreset(name, spec) },
				func() { toggleProxyProtocol(name, spec) },
				func() { editLimits(name, spec) },
			}
			opts = append(opts, tui.Option{
				Title: "Forward UDP: " + onOff(spec.AcceptUDP),
				Desc:  "off unless you need UDP",
			})
			actions = append(actions, func() { toggleAcceptUDP(name, spec) })
			opts = append(opts, tui.Option{
				Title: "Fallback Transports",
				Desc:  "tried when this one is blocked",
			})
			actions = append(actions, func() { changeFallbackTransports(name, spec) })
			if !isDatagram(spec.Transport) {
				opts = append(opts, tui.Option{
					Title: "TCP MSS Clamp",
					Desc:  "for a path that drops full-size packets",
				})
				actions = append(actions, func() { editMSS(name, spec) })
			}
			if spec.Transport == "pck" {
				opts = append(opts, tui.Option{
					Title: "TCP Flags",
					Desc:  "this end's packet flags",
				})
				actions = append(actions, func() { editPckFlags(name, spec) })
			}
			if needsTLS(spec.Transport) {
				opts = append(opts, tui.Option{
					Title: "Certificate",
					Desc:  "self-signed or Let's Encrypt",
				})
				actions = append(actions, func() { editCertificate(name, spec) })
			}
			if len(ConfigHistory(name)) > 0 {
				opts = append(opts, tui.Option{
					Title: "Undo A Change",
					Desc:  "restore an earlier config",
				})
				actions = append(actions, func() { editConfigHistory(name) })
			}
			idx := tui.ChooseOpt("Edit", opts)
			if idx < 0 || idx >= len(actions) {
				return
			}
			actions[idx]()
		} else {
			tui.Info("Iran Address   : " + spec.RemoteAddr)
			tui.Info("Transport      : " + transportLabel(spec.Transport))
			tui.Info("Backups        : " + fallbackSummary(spec.FallbackAddrs))
			tui.Info("Fallbacks      : " + chainSummary(spec.Transport, spec.FallbackTransports))
			tui.Info("Preset         : " + presetLabel(spec.Preset))
			tui.Info("Load Balancing : " + onOff(spec.LoadBalance))
			if !isDatagram(spec.Transport) {
				tui.Info("TCP MSS Clamp  : " + mssLabel(spec.MSS))
			}
			if spec.Transport == "pck" {
				tui.Info("TCP Flags      : " + pckFlagSummary(spec.PckFlags))
			}
			fmt.Println()
			opts := []tui.Option{
				{Title: "Tunnel Port", Desc: "must match the Iran side"},
				{Title: "Iran Address", Desc: "IP or domain"},
				{Title: "Transport", Desc: "keeps the token"},
				{Title: "Backup Addresses", Desc: "failover when the main IP is blocked"},
				{Title: "Fallback Transports", Desc: "failover when the transport is blocked"},
				{Title: "Preset", Desc: "Balance, Turbo or Aggressive"},
				{Title: "Load Balancing", Desc: "use all addresses at once"},
			}
			actions := []func(){
				func() { changeTunnelPort(name, spec) },
				func() { changeClientHost(name, spec) },
				func() { changeTunnelTransport(name, spec) },
				func() { changeFallbackAddrs(name, spec) },
				func() { changeFallbackTransports(name, spec) },
				func() { changeTunnelPreset(name, spec) },
				func() { toggleLoadBalance(name, spec) },
			}
			if !isDatagram(spec.Transport) {
				opts = append(opts, tui.Option{
					Title: "TCP MSS Clamp",
					Desc:  "for a path that drops full-size packets",
				})
				actions = append(actions, func() { editMSS(name, spec) })
			}
			if spec.Transport == "pck" {
				opts = append(opts, tui.Option{
					Title: "TCP Flags",
					Desc:  "this end's packet flags",
				})
				actions = append(actions, func() { editPckFlags(name, spec) })
			}
			if len(ConfigHistory(name)) > 0 {
				opts = append(opts, tui.Option{
					Title: "Undo A Change",
					Desc:  "restore an earlier config",
				})
				actions = append(actions, func() { editConfigHistory(name) })
			}
			idx := tui.ChooseOpt("Edit", opts)
			if idx < 0 || idx >= len(actions) {
				return
			}
			actions[idx]()
		}
	}
}

// changeTunnelPort prompts for and applies a new tunnel (control) port.
func changeTunnelPort(name string, spec TunnelSpec) {
	// The default offered back is what this tunnel currently binds, written
	// the way it would be typed: the address as well, when it has one, so
	// accepting the default cannot silently widen a pinned tunnel to every
	// interface.
	cur := addrPort(spec.BindAddr)
	if spec.Role == "client" {
		cur = addrPort(spec.RemoteAddr)
	} else if h := bindHostOf(spec.BindAddr); h != "" {
		cur = net.JoinHostPort(h, cur)
	}
	fmt.Println()
	if spec.Role == "server" {
		tui.Info("Port Alone = All Addresses; 1.2.3.4:443 = One.")
	}
	entered := tui.PromptDefault("New Tunnel Port", cur)
	if entered == cur {
		return
	}
	bind, err := parseTunnelBind(entered)
	if err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	port := bind.Port
	if spec.Role == "client" && bind.HasHost() {
		// On a client this field is the port on the SERVER, not something
		// bound here — so an address in it is almost certainly aimed at the
		// wrong question.
		tui.Error("This side binds nothing — use Iran Address instead.")
		tui.PressEnter()
		return
	}
	// Check the protocol the transport actually binds, on the address it
	// binds: a UDP-based tunnel is unaffected by whatever holds the same TCP
	// port, and a tunnel pinned to one address is unaffected by a listener on
	// another.
	if spec.Role == "server" {
		if bind.HasHost() && !localAddrExists(bind.Host) {
			tui.Warn(bind.Host + " is not on this server.")
		}
		if TunnelPortInUse(spec.Transport, bind.Addr(false)) {
			tui.Error(fmt.Sprintf("%s is already in use on this machine.", bind.Addr(false)))
			tui.PressEnter()
			return
		}
	}
	if err := EditTunnel(name, "", entered, nil); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success(fmt.Sprintf("Tunnel Port: %s — Restarted.", entered))
	if spec.Role == "server" {
		tui.Warn("Set Port " + port + " On The Kharej Too.")
	}
	tui.PressEnter()
}

// changeForwardedPorts prompts for and applies a new forwarded-ports list.
func changeForwardedPorts(name string, spec TunnelSpec) {
	fmt.Println()
	tui.Info("Current: " + strings.Join(VisiblePorts(spec.Ports, spec.Token), ", "))
	raw := tui.Prompt("Forwarded Ports (Full List, e.g. 443,8080): ")
	ports := parsePorts(raw)
	if len(ports) == 0 {
		tui.Error("No valid ports.")
		tui.PressEnter()
		return
	}
	if err := EditTunnel(name, "", "", ports); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Forwarded Ports Saved — Restarted.")
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
	tui.Info("Current: " + transportLabel(selectedTransport(spec)) + " (The Other Side Must Match)")
	fmt.Println()

	newTransport := chooseTransport()
	if newTransport == "" {
		return
	}
	if managedTransport(newTransport) {
		editManagedSettings(name, spec, newTransport)
		return
	}
	wasManaged := managedTransport(selectedTransport(spec))
	if newTransport == spec.Transport && !wasManaged {
		tui.Info("Already In Use.")
		tui.PressEnter()
		return
	}
	if spec.Role == "server" && needsTLS(newTransport) {
	}
	if !tui.Confirm(fmt.Sprintf("Switch To %s", transportLabel(newTransport)), true) {
		return
	}

	var err error
	if wasManaged {
		endpoint := managedEndpoint(spec)
		clearManagedTransport(&spec)
		if spec.Role == "server" {
			spec.BindAddr = endpoint
		} else {
			spec.RemoteAddr = endpoint
		}
		err = switchTransport(&spec, newTransport)
		if err == nil {
			err = applySpec(spec)
		}
	} else {
		err = ChangeTransport(name, newTransport)
	}
	if err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Transport: " + transportLabel(newTransport) + " — Restarted.")
	tui.Warn("Switch The Other Side Too.")
	tui.PressEnter()
}

func editManagedSettings(name string, spec TunnelSpec, chosen string) {
	label := "Public HTTPS Listen Address (IP:Port)"
	if spec.Role == "client" {
		label = "Iran HTTPS Address (Hostname Or IP:Port)"
	}
	public := tui.PromptDefault(label, managedEndpoint(spec))
	host := addrHost(public, "")
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = ""
	}
	if spec.AcceptUDP || len(spec.FallbackAddrs) > 0 || len(spec.FallbackTransports) > 0 ||
		spec.Proxy != "" || spec.Interface != "" || spec.LocalAddr != "" || spec.SOMark != 0 || spec.LoadBalance || spec.HealthFailover {
		tui.Warn("This HTTPS Transport Supports TCP Only; UDP, Proxies, Routing Bindings And Fallbacks Will Be Disabled.")
		if !tui.Confirm("Continue With TCP Forwarding Only", false) {
			return
		}
	}
	if !setupManagedCarrier(&spec, chosen, public, host) {
		tui.PressEnter()
		return
	}
	summariseReverse(spec, "", "")
	if !tui.Confirm("Save HTTPS Settings And Restart This Side", true) {
		return
	}
	tui.StopIfInputGone()
	if err := applySpec(spec); err != nil {
		tui.Error("Failed: " + err.Error())
	} else {
		tui.Success("HTTPS Settings Saved — Restarted.")
		tui.Warn("Configure The Other Side With The Matching Settings.")
	}
	tui.PressEnter()
}

// Managed carriers keep the internal target private. Their public endpoint,
// certificate and credentials are edited together so one restart applies them.
func editManagedTunnelMenu(name string, spec TunnelSpec) bool {
	chosen := selectedTransport(spec)
	tui.Info("Transport       : " + transportLabel(chosen))
	tui.Info("HTTPS Endpoint  : " + managedEndpoint(spec))
	internal := spec.BindAddr
	if spec.Role == "client" {
		internal = spec.RemoteAddr
	}
	tui.Info("Internal Target : " + internal)
	opts := []tui.Option{
		{Title: "HTTPS Settings", Desc: "public endpoint, internal port, certificate and credentials"},
		{Title: "Transport", Desc: "change the protocol on both sides"},
		{Title: "Preset", Desc: "tune the TCP engine"},
		{Title: "TCP MSS Clamp", Desc: "for a path that drops full-size packets"},
	}
	actions := []func(){
		func() { editManagedSettings(name, spec, chosen) },
		func() { changeTunnelTransport(name, spec) },
		func() { changeTunnelPreset(name, spec) },
		func() { editMSS(name, spec) },
	}
	if spec.Role == "server" {
		tui.Info("Forwarded Ports : " + strings.Join(VisiblePorts(spec.Ports, spec.Token), ", "))
		opts = append(opts, tui.Option{Title: "Forwarded Ports", Desc: "TCP services on Kharej"}, tui.Option{Title: "Limits", Desc: "connections and bandwidth"})
		actions = append(actions, func() { changeForwardedPorts(name, spec) }, func() { editLimits(name, spec) })
	}
	if len(ConfigHistory(name)) > 0 {
		opts = append(opts, tui.Option{Title: "Undo A Change", Desc: "restore an earlier config"})
		actions = append(actions, func() { editConfigHistory(name) })
	}
	idx := tui.ChooseOpt("Edit HTTPS Tunnel", opts)
	if idx < 0 || idx >= len(actions) {
		return false
	}
	actions[idx]()
	return true
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
	tui.Title("Load Balancing")
	tui.Warn("Spreads connections over all addresses. Every address must reach the same server.")
	tui.Info("Backups : " + fallbackSummary(spec.FallbackAddrs))
	tui.Info("Now     : " + onOff(spec.LoadBalance))
	fmt.Println()

	want := !spec.LoadBalance
	verb := "Enable"
	if !want {
		verb = "Disable"
	}
	if !tui.Confirm(verb+" Load Balancing", false) {
		return
	}
	if err := SetLoadBalance(name, want); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Load Balancing: " + onOff(want) + " — Restarted.")
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
	tui.Title("Limits — " + name)
	fmt.Println()

	maxConns := tui.PromptInt("Max Connections (0 = No Limit)", spec.MaxConnections)
	bandwidth := tui.PromptInt("Bandwidth Mbit/s (0 = No Limit)", spec.BandwidthMbps)

	if maxConns == spec.MaxConnections && bandwidth == spec.BandwidthMbps {
		tui.Info("No Change.")
		tui.PressEnter()
		return
	}
	if maxConns > 0 && maxConns < 10 {
		tui.Warn(fmt.Sprintf("%d is very low — one browser opens more.", maxConns))
		if !tui.Confirm("Use It Anyway", false) {
			return
		}
	}
	if err := SetLimits(name, maxConns, bandwidth); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Limits Saved — Restarted.")
	tui.PressEnter()
}

// editPckFlags changes the TCP flags the packet carrier stamps on what it
// sends.
func editPckFlags(name string, spec TunnelSpec) {
	fmt.Println()
	tui.Title("TCP Flags — " + name)
	tui.Info("Now: " + pckFlagSummary(spec.PckFlags) + " (This End Only; Default push+ack)")
	fmt.Println()

	opts := network.SuggestedTCPFlagCycles()
	menu := make([]tui.Option, len(opts))
	for i, o := range opts {
		menu[i] = tui.Option{Title: o.Value, Desc: o.Desc}
	}
	i := tui.ChooseOpt("Flag Pattern", menu)
	if i < 0 {
		return
	}
	var flags []string
	if i > 0 {
		flags = strings.Split(opts[i].Value, ",")
	}
	if err := SetPckFlags(name, flags); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Flags: " + pckFlagSummary(flags) + " — Restarted.")
	tui.PressEnter()
}

// toggleAcceptUDP turns forwarding of UDP on the exposed ports on or off. It is
// the CLI's way to undo the v1.7.1 default on an existing tunnel, since a tunnel
// created then carries an explicit accept_udp = true that only an edit clears.
func toggleAcceptUDP(name string, spec TunnelSpec) {
	fmt.Println()
	tui.Title("Forward UDP — " + name)
	tui.Warn("Only for UDP services (VPN, games); on ws/mux it can starve TCP.")
	tui.Info("Now: " + onOff(spec.AcceptUDP))
	fmt.Println()

	want := !spec.AcceptUDP
	verb := "Enable"
	if !want {
		verb = "Disable"
	}
	if !tui.Confirm(verb+" UDP Forwarding", false) {
		return
	}
	if err := SetAcceptUDP(name, want); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("UDP Forwarding: " + onOff(want) + " — Restarted.")
	tui.PressEnter()
}

// editMSS sets the tunnel's TCP segment clamp — the fix the path-MTU check in
// Diagnose asks for, and the reason this entry exists at all.
func editMSS(name string, spec TunnelSpec) {
	fmt.Println()
	tui.Title("TCP MSS Clamp — " + name)
	tui.Warn("Set only when Health Check asks; use the same value on both ends.")
	tui.Info("Now: " + mssLabel(spec.MSS))

	mss := tui.PromptInt("MSS Bytes (0 = Auto)", spec.MSS)
	if mss == spec.MSS {
		tui.Info("No Change.")
		tui.PressEnter()
		return
	}
	if err := SetMSS(name, mss); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("MSS Clamp: " + mssLabel(mss) + " — Restarted.")
	tui.Warn("Set The Same Value On The Other Side.")
	tui.PressEnter()
}

// toggleProxyProtocol switches forwarding of the real client IP on or off.
func toggleProxyProtocol(name string, spec TunnelSpec) {
	fmt.Println()
	tui.Title("Real Client IP (PROXY Protocol)")
	tui.Warn("Enable \"Accept Proxy Protocol\" on the service first, or every connection breaks.")
	tui.Info("Now: " + onOff(spec.ProxyProtocol))
	fmt.Println()

	want := !spec.ProxyProtocol
	verb := "Enable"
	if !want {
		verb = "Disable"
	}
	if !tui.Confirm(verb+" Real Client IP", false) {
		return
	}
	if err := SetProxyProtocol(name, want); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Real Client IP: " + onOff(want) + " — Restarted.")
	tui.PressEnter()
}

// changeTunnelPreset re-applies a whole performance profile to a tunnel.
func changeTunnelPreset(name string, spec TunnelSpec) {
	fmt.Println()
	tui.Info("Current: " + presetLabel(spec.Preset) + " (Use The Same On Both Sides)")
	fmt.Println()

	newPreset := choosePreset(spec.Transport)
	if newPreset == spec.Preset {
		tui.Info("Already In Use.")
		tui.PressEnter()
		return
	}
	if !tui.Confirm(fmt.Sprintf("Apply %s", presetLabel(newPreset)), true) {
		return
	}

	if err := ChangePreset(name, newPreset); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Preset: " + presetLabel(newPreset) + " — Restarted.")
	tui.Warn("Apply The Same Preset On The Other Side.")
	tui.PressEnter()
}

// changeFallbackAddrs manages the client's backup server addresses. This is what
// keeps a tunnel alive when the main server IP is filtered: the client walks the
// list until one address answers.
func changeFallbackAddrs(name string, spec TunnelSpec) {
	fmt.Println()
	tui.Title("Backup Addresses")
	tui.Info("Main : " + spec.RemoteAddr)
	tui.Info("Now  : " + fallbackSummary(spec.FallbackAddrs))
	fmt.Println()

	raw := tui.Prompt("Backup Addresses (Full List, Blank = None): ")
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
		tui.Success("Backups Cleared — Restarted.")
	} else {
		tui.Success(fmt.Sprintf("%d Backup(s) Saved — Restarted.", len(addrs)))
	}
	tui.PressEnter()
}

// changeClientHost prompts for and applies a new server address on a client.
func changeClientHost(name string, spec TunnelSpec) {
	fmt.Println()
	host := tui.PromptDefault("Iran IP Or Domain", addrHost(spec.RemoteAddr, ""))
	if strings.TrimSpace(host) == "" {
		tui.Error("An address is required.")
		tui.PressEnter()
		return
	}
	if err := EditTunnel(name, host, "", nil); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Iran Address Saved — Restarted.")
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
	tui.Title("Certificate — " + name)
	tui.Info("Now: " + certSummary(s))
	fmt.Println()

	idx := tui.ChooseOpt("Certificate", []tui.Option{
		{Title: "Self-Signed", Desc: "works on a bare IP — default"},
		{Title: "Let's Encrypt", Desc: "needs a domain pointing here; needed for a CDN"},
	})

	switch idx {
	case 0:
		if s.ACMEDomain == "" {
			tui.Info("Already Self-Signed.")
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

	tui.Success("Certificate Saved — Restarted.")
	if s.ACMEDomain != "" {
		fmt.Println()
		tui.Warn("Issued On The First Connection. Log: journalctl -u " + app.ServiceName(name) + " -n 50")
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
