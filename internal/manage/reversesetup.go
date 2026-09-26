package manage

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/optimize"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/utils/network"
)

// The reverse wizard, in the direct wizard's style.
//
// The Iran server is set up first. Its questions are short and in the order
// the operator has the answers — its own address, the port, what to forward,
// the name and the token — then the transport's own few, the tuning, and one
// short summary with the kharej's setup link under it. The kharej server picks
// the same transport and pastes that link; typing it in by hand stays possible.
//
// Each transport keeps what is its own: a certificate for WSS, the flag
// pattern for PCK, the edge address for a WebSocket client, the PROXY header
// where it can be carried. Only the wording and the order are shared.

// SetupServer runs the Iran side of a reverse tunnel: it listens, and exposes
// the forwarded ports.
func SetupServer() {
	tui.Clear()
	tui.Title("Reverse Tunnel — Iran")
	fmt.Println()

	transport := chooseTransport()
	if transport == "" {
		return
	}

	// AcceptUDP starts off: a forwarded port carries TCP only unless the
	// operator turns UDP on, which is asked for below. See
	// config.ServerConfig.ForwardsUDP.
	s := TunnelSpec{Role: "server", Transport: transport}

	// The address the kharej will dial, which the setup link carries to it.
	// The machine's own public address is the default; a domain, or another
	// address when this server is reached through one, replaces it.
	detected := PublicIPv4()
	if detected == "-" {
		detected = ""
	}
	host := strings.Trim(strings.TrimSpace(tui.PromptDefault("Iran IP Or Domain (What Kharej Dials)", detected)), "[]")

	// A port alone listens on every address; "85.10.11.51:443" pins it to
	// one, so another service can hold the same port on another address.
	bind, err := parseTunnelBind(tui.Prompt("Tunnel Port: "))
	if err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	// Binding the IPv6 wildcard accepts IPv4 as well on a normal dual-stack
	// host, so this is "IPv6 too" rather than "IPv6 instead".
	ipv6 := false
	if !bind.HasHost() {
		ipv6 = tui.Confirm("Listen On IPv6 As Well", false)
	} else if !localAddrExists(bind.Host) {
		// A warning, not a refusal: a floating address or one that arrives
		// with a later interface is a real setup. See localAddrExists.
		tui.Warn(bind.Host + " is not on any interface here yet — the tunnel fails to bind until it is.")
	}
	s.BindAddr = bind.Addr(ipv6)
	if host == "" && bind.HasHost() {
		host = bind.Host
	}

	// A bare 443 is Iran's 443 to the kharej's own 127.0.0.1:443. Elsewhere:
	// 443=127.0.0.1:2096; several backends: 443=127.0.0.1:2096|127.0.0.1:2097.
	s.Ports = parsePorts(tui.Prompt("Forwarded Ports (e.g. 443, 8080=127.0.0.1:2096): "))
	if len(s.Ports) == 0 {
		tui.Error("No valid ports entered.")
		tui.PressEnter()
		return
	}
	if err := validatePortSpecs(s.Ports); err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	if busy := reverseBusyPorts(s.Ports, s.BindAddr, s.Transport); len(busy) > 0 {
		tui.Error("Already in use on this server: " + strings.Join(busy, ", ") +
			" — the tunnel port or the web panel's own port is the usual one. Pick other ports.")
		tui.PressEnter()
		return
	}

	s.Name = uniqueName(tui.PromptDefault("Tunnel Name", "server-"+bind.Port))

	// Iran makes the token; the setup link carries it to kharej.
	s.Token = strings.TrimSpace(tui.PromptDefault("Security Token", randomToken(64)))
	if s.Token == "" {
		tui.Error("A token is required.")
		tui.PressEnter()
		return
	}

	// Off by default: yes for Xray/Shadowsocks UDP, WireGuard, DNS or games;
	// no for a plain web tunnel, where a browser's QUIC would crowd out the
	// TCP forwards. See config.ServerConfig.ForwardsUDP.
	// The udp transport's forwarded ports are UDP by nature, so it has
	// nothing to ask here.
	if transport != "udp" {
		s.AcceptUDP = tui.Confirm("Carry UDP As Well As TCP On Those Ports", false)
	}

	// The transport's own questions.
	if needsTLS(transport) && !setupServerTLS(&s) {
		return
	}
	askSimpleAuth(&s, transport)
	askPck(&s)
	askProxyProtocol(&s)

	ApplyPreset(&s, choosePreset(s.Transport))
	if tui.Confirm("Fine-Tune The Advanced Settings", false) {
		applyManualTuning(&s)
	}

	link := pendingReverseLink(s, host)
	summariseReverse(s, host, link)
	if !tui.Confirm("Create This Tunnel", true) {
		return
	}
	if !finishSetup(s) {
		return
	}
	if link != "" {
		tui.Info("Paste the setup link above on the kharej server. It is shown again under")
		tui.Info("Manage Tunnels → this tunnel → Setup Link.")
	}
	tui.PressEnter()
}

// SetupClient runs the kharej side of a reverse tunnel: it dials the Iran
// server and hands each connection to the real service.
func SetupClient() {
	tui.Clear()
	tui.Title("Reverse Tunnel — Kharej")
	fmt.Println()

	transport := chooseTransport()
	if transport == "" {
		return
	}

	switch tui.ChooseOpt("How Do You Want To Set Up This Side?", []tui.Option{
		{Title: "Setup Link", Desc: "recommended — paste the link the Iran server printed; everything is filled in"},
		{Title: "Manual", Desc: "type the Iran address, the port and the token yourself"},
	}) {
	case 0:
		setupClientFromLink(transport)
		return
	case 1:
	default:
		return
	}

	s := TunnelSpec{Role: "client", Transport: transport}

	remoteHost := strings.Trim(strings.TrimSpace(tui.Prompt("Iran IP Or Domain: ")), "[]")
	remotePort := strings.TrimSpace(tui.Prompt("Tunnel Port: "))
	if remoteHost == "" || !validPort(remotePort) {
		tui.Error("Invalid server address or port.")
		tui.PressEnter()
		return
	}
	// JoinHostPort adds the brackets an IPv6 literal needs, and leaves a
	// hostname or IPv4 address alone.
	s.RemoteAddr = net.JoinHostPort(remoteHost, remotePort)
	if !checkServerAddress(remoteHost, transport, remotePort) {
		return
	}

	s.Name = uniqueName(tui.PromptDefault("Tunnel Name", "client-"+remotePort))

	// No default: a token that does not match looks exactly like a blocked
	// port, and the Iran server is where it was made.
	s.Token = strings.TrimSpace(tui.Prompt("Security Token (From The Iran Server): "))
	if s.Token == "" {
		tui.Error("Set up the Iran server first: it makes the token, and a setup link")
		tui.Error("that fills in this whole side for you.")
		tui.PressEnter()
		return
	}

	// The transport's own questions.
	if isWS(transport) {
		// A CDN edge (e.g. Cloudflare) to connect to instead of resolving the
		// server address directly.
		s.EdgeIP = strings.TrimSpace(tui.PromptDefault("Edge IP (Optional, For A CDN)", ""))
	}
	askSimpleAuth(&s, transport)
	askPck(&s)
	askConnectionOptions(&s, remotePort)

	ApplyPreset(&s, choosePreset(s.Transport))
	if tui.Confirm("Fine-Tune The Advanced Settings", false) {
		applyManualTuning(&s)
	}

	summariseReverse(s, "", "")
	if !tui.Confirm("Create This Tunnel", true) {
		return
	}
	if finishSetup(s) {
		tui.PressEnter()
	}
}

// askConnectionOptions is the kharej's optional connectivity: a proxy to reach
// Iran through, an interface to leave by, and backup addresses. Most tunnels
// need none of it, so it is one question that defaults to no.
func askConnectionOptions(s *TunnelSpec, remotePort string) {
	if !tui.Confirm("Optional Connection Settings (Proxy, Interface, Backup Addresses)", false) {
		return
	}
	// A TCP proxy cannot relay the datagram transports' UDP.
	if !isDatagram(s.Transport) {
		for {
			raw := strings.TrimSpace(tui.PromptDefault("Proxy URL (socks5://… Or http://…, Blank = None)", ""))
			if raw == "" {
				break
			}
			if _, err := network.ParseProxy(raw); err != nil {
				tui.Error(fmt.Sprintf("%v", err))
				continue
			}
			s.Proxy = raw
			break
		}
		// Only worth asking on a machine with somewhere else to go.
		if names := routableInterfaces(); len(names) > 1 {
			tui.Info("Interfaces: " + strings.Join(names, ", "))
			for {
				raw := strings.TrimSpace(tui.PromptDefault("Interface (Blank = Automatic)", ""))
				if raw == "" {
					break
				}
				if _, err := net.InterfaceByName(raw); err != nil {
					tui.Error(fmt.Sprintf("no such interface: %v", err))
					continue
				}
				s.Interface = raw
				break
			}
			s.LocalAddr = strings.TrimSpace(tui.PromptDefault("Source Address (Optional)", ""))
		}
	}

	// Tried in turn when the main address stops answering; a bare IP reuses
	// the main port.
	if raw := strings.TrimSpace(tui.PromptDefault("Backup Addresses (Comma Separated, Optional)", "")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, _, err := net.SplitHostPort(part); err != nil {
				part = net.JoinHostPort(strings.Trim(part, "[]"), remotePort)
			}
			s.FallbackAddrs = append(s.FallbackAddrs, part)
		}
	}
	if len(s.FallbackAddrs) > 0 {
		// Every address must reach the SAME server — a second IP of it,
		// another port, or a CDN edge in front of it.
		if tui.Confirm("Automatic Failover To The Healthiest Address", false) {
			s.HealthFailover = true
		} else {
			s.LoadBalance = tui.Confirm("Load Balance Over All Addresses", false)
		}
	}
}

// setupClientFromLink builds the kharej side from the Iran server's setup
// link: the token, the port, the transport and every paired setting come
// from it, and only a name is asked.
func setupClientFromLink(chosen string) {
	link, err := DecodeShareLink(tui.Prompt("Setup Link: "))
	if err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	if link.Kind != "reverse" || !strings.EqualFold(link.From, "iran") {
		tui.Error("This link is for the " + link.PeerSide() + " side of a " + link.Kind +
			" tunnel, not for the kharej side of a reverse one.")
		tui.PressEnter()
		return
	}
	if link.Tr != chosen {
		tui.Warn("This link is for a " + transportLabel(link.Tr) + " tunnel, not " + transportLabel(chosen) +
			" — setting up " + transportLabel(link.Tr) + ", as the Iran server has it.")
	}

	host := strings.Trim(strings.TrimSpace(link.Host), "[]")
	if host == "" {
		host = strings.Trim(strings.TrimSpace(tui.Prompt("Iran IP Or Domain: ")), "[]")
		if host == "" {
			tui.Error("An address is required.")
			tui.PressEnter()
			return
		}
	}

	s := reverseClientFromLink(link, host)
	s.Name = uniqueName(tui.PromptDefault("Tunnel Name", s.Name))

	// What the link cannot carry, because it is this side's own answer: a CDN
	// edge to reach Iran through, PCK's flag pattern and interface, a proxy or
	// backup addresses. The same questions the manual path asks.
	if isWS(s.Transport) {
		s.EdgeIP = strings.TrimSpace(tui.PromptDefault("Edge IP (Optional, For A CDN)", ""))
	}
	askPck(&s)
	askConnectionOptions(&s, link.Port)

	summariseReverse(s, "", "")
	if !tui.Confirm("Create This Tunnel", true) {
		return
	}
	if finishSetup(s) {
		tui.Info("It comes up as soon as it reaches the Iran server.")
		tui.PressEnter()
	}
}

// reverseClientFromLink is the kharej's spec, mirrored from the Iran server's
// link. Pure, so the mirroring can be tested without a terminal.
func reverseClientFromLink(link ShareLink, host string) TunnelSpec {
	s := TunnelSpec{
		Role:       "client",
		Transport:  link.Tr,
		RemoteAddr: net.JoinHostPort(host, link.Port),
		Token:      link.Tok,
		Name:       peerName(link),
	}
	// The Iran default "server-443" would suggest "server-443-kharej"; the
	// kharej's own default is "client-443".
	if strings.HasPrefix(link.Name, "server-") || s.Name == "" || !validName(s.Name) {
		s.Name = "client-" + link.Port
	}
	preset := link.Preset
	if preset == "" {
		preset = PresetTurbo
	}
	ApplyPreset(&s, preset)
	// What the two ends must agree on, taken as the Iran server has it rather
	// than as this side's preset would have it.
	s.MSS = link.MSS
	s.SimpleAuth = link.SimpleAuth
	if link.MuxVer > 0 {
		s.MuxVersion = link.MuxVer
	}
	if link.Tr == "kcp" {
		s.KCPDataShards, s.KCPParityShards = link.FECData, link.FECParity
	}
	return s
}

// pendingReverseLink is the setup link the Iran tunnel will have once it is
// written, built from the config about to be written, with the address the
// kharej dials. Empty when it cannot be built; the link is then under Manage.
func pendingReverseLink(s TunnelSpec, host string) string {
	var c config.Config
	if _, err := toml.Decode(s.Render(), &c); err != nil {
		return ""
	}
	link, err := shareLinkOf(s.Name, host, c)
	if err != nil {
		return ""
	}
	return link
}

// summariseReverse is the one screen before "Create This Tunnel": the
// transport in the menu's words, one fact a line, and on Iran the kharej's
// setup link under it.
func summariseReverse(s TunnelSpec, host, link string) {
	row := func(label, value string) {
		fmt.Printf("%s%-16s:%s %s\n", tui.Gray, label, tui.Reset, tui.Color(tui.White, value))
	}
	fmt.Println()
	tui.Rule()
	if s.Role == "server" {
		tui.Title("Reverse " + transportLabel(s.Transport))
	} else {
		tui.Title("Reverse " + transportLabel(s.Transport) + " (Kharej)")
	}
	fmt.Println()

	if s.Role == "server" {
		row("Listens On", s.BindAddr)
		if host != "" {
			row("Kharej Dials", net.JoinHostPort(host, addrPort(s.BindAddr)))
		}
		ports := strings.Join(s.Ports, ", ")
		if s.AcceptUDP {
			ports += "  (TCP + UDP)"
		}
		row("Forwarded Ports", ports)
		row("Kharej Serves", strings.Join(forwardTargets(s.Ports), ", "))
		if needsTLS(s.Transport) {
			switch {
			case s.ACMEDomain != "":
				row("TLS", "Let's Encrypt for "+s.ACMEDomain)
			case strings.HasPrefix(s.TLSCert, app.ConfigDir):
				row("TLS", "Self-Signed")
			case s.TLSCert != "":
				row("TLS", s.TLSCert)
			}
		}
		if s.ProxyProtocol {
			row("Real Client IP", "PROXY protocol on")
		}
	} else {
		row("Dials", s.RemoteAddr)
		if s.EdgeIP != "" {
			row("Edge IP", s.EdgeIP)
		}
		if s.Proxy != "" {
			row("Proxy", s.Proxy)
		}
		if len(s.FallbackAddrs) > 0 {
			mode := "failover"
			switch {
			case s.HealthFailover:
				mode = "healthiest first"
			case s.LoadBalance:
				mode = "load balanced"
			}
			row("Backup Addresses", strings.Join(s.FallbackAddrs, ", ")+"  ("+mode+")")
		}
	}
	if s.SimpleAuth {
		row("Simple Auth", "on")
	}
	if len(s.PckFlags) > 0 {
		row("Flag Pattern", strings.Join(s.PckFlags, ","))
	}
	row("Tuning", presetLabel(s.Preset))
	row("Config File", app.ConfigPath(s.Name))
	if link != "" {
		fmt.Println()
		tui.Info("Setup Link (Setup Kharej → Reverse → The Same Transport → Setup Link) :")
		fmt.Println(tui.Color(tui.Bold+tui.White, link))
	}
	tui.Rule()
	fmt.Println()
}

// forwardTargets is each mapping as "exposed → what the kharej must have
// listening", the one line that settles where a bare "443" goes.
func forwardTargets(ports []string) []string {
	var out []string
	for _, p := range ports {
		exposed, dest, found := strings.Cut(strings.TrimSpace(p), "=")
		exposed = strings.TrimSpace(exposed)
		if !found {
			out = append(out, exposed+" → 127.0.0.1:"+exposed)
			continue
		}
		var parts []string
		for _, d := range strings.Split(strings.TrimSpace(dest), "|") {
			if d = strings.TrimSpace(d); d == "" {
				continue
			}
			if !strings.Contains(d, ":") {
				d = "127.0.0.1:" + d
			}
			parts = append(parts, d)
		}
		out = append(out, exposed+" → "+strings.Join(parts, "|"))
	}
	return out
}

// reverseBusyPorts lists the forwarded ports this Iran server could not listen
// on: one something on this machine already holds, or one that is the tunnel's
// own port. The direct wizard has refused these since v1.8.4 (busyForwardPorts);
// the reverse one wrote the tunnel anyway and its listener failed to bind.
func reverseBusyPorts(specs []string, bindAddr, transport string) []string {
	bindHost, bindPort, _ := net.SplitHostPort(bindAddr)
	wildcard := func(h string) bool { return h == "" || h == "0.0.0.0" || h == "::" }
	var busy []string
	for _, spec := range specs {
		local, _, _ := strings.Cut(strings.TrimSpace(spec), "=")
		local = strings.TrimSpace(local)
		host, lo, hi := "", local, local
		if h, p, err := net.SplitHostPort(local); err == nil {
			host, lo, hi = h, p, p
		} else if a, b, ok := strings.Cut(local, "-"); ok {
			lo, hi = strings.TrimSpace(a), strings.TrimSpace(b)
		}
		from, err1 := strconv.Atoi(lo)
		to, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil || to < from {
			continue // validatePortSpecs has already said so
		}
		for p := from; p <= to; p++ {
			port := strconv.Itoa(p)
			// The tunnel's own listener is not up yet, so asking the kernel
			// would say the port is free. A datagram transport listens on UDP
			// and leaves the TCP port to the forward.
			if !isDatagram(transport) && port == bindPort &&
				(wildcard(bindHost) || wildcard(host) || bindHost == host) {
				busy = append(busy, port)
				continue
			}
			if portHeld(net.JoinHostPort(host, port)) {
				busy = append(busy, port)
			}
		}
	}
	return busy
}

// finishSetup persists the tunnel, applies system-level tuning, and reports
// the result. It reports false, having said why and waited for Enter, when
// the tunnel was not created; on success the caller finishes the screen.
func finishSetup(s TunnelSpec) bool {
	// The same refusal the panel makes, at the same point: before anything is
	// written. Two creation paths that disagree about what is allowed is how a
	// check ends up covering half the product — see portClash for what this is
	// for.
	addr := s.BindAddr
	if s.Role == "client" {
		addr = s.RemoteAddr
	}
	if why := portClash(s.Role, addr, s.Name); why != "" {
		fmt.Println()
		tui.Error(why)
		fmt.Println()
		tui.Info("Nothing was written. Run setup again with a different port.")
		tui.PressEnter()
		return false
	}

	tui.Info("Applying system network optimizations...")
	optimize.ApplyQuiet(ReservedPorts())

	service, err := s.Save()
	if err != nil {
		tui.Error("Failed to create tunnel: " + err.Error())
		tui.PressEnter()
		return false
	}

	fmt.Println()
	if IsActive(service) {
		tui.Success(fmt.Sprintf("Tunnel %q is up and running (%s).", s.Name, service))
	} else {
		tui.Warn(fmt.Sprintf("Tunnel %q created but not active yet — check logs.", s.Name))
	}
	return true
}
