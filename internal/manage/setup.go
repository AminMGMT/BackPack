package manage

import (
	"fmt"
	"net"
	"strings"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/optimize"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/utils/network"
)

// transportEntry is one selectable transport. An empty value marks an entry
// that is listed for orientation but cannot be chosen yet.
type transportEntry struct {
	label, desc, value string
}

// transportGroups organises the transports into the families they actually
// belong to, so the setup menu asks "which kind of connection?" before asking
// for a specific variant.
//
// The Experimental family is deliberately its own thing rather than a variant
// tucked under another: what it holds is not a flavour of TCP or UDP but a
// different idea about how to move bytes at all — xDi rides in ICMP, which is
// neither — and it is where anything else of that sort will go, so it should
// read as "here be things still being proven", not as a footnote to UDP.
var transportGroups = []struct {
	label, desc string
	entries     []transportEntry
}{
	{"TCP", "reliable and simple — the safe default", []transportEntry{
		{"TCP", "plain & fast — start here if unsure", "tcp"},
		{"TCP Mux", "many streams over few connections — multiplexed", "tcpmux"},
		{"TCP + Stealth", "encrypted with no fingerprint — hardest to detect, for heavy filtering", "stealth"},
		{"TCP + PCK", "builds its own TCP packets, below the kernel — for a path where a normal TCP flow is reset or throttled; Linux, needs root", "pck"},
	}},
	{"UDP", "lower latency, better on lossy or throttled links", []transportEntry{
		{"UDP", "raw datagrams — for UDP-based services", "udp"},
		{"UDP + KCP + FEC", "low-latency gaming tunnel — reliable UDP with always-on error correction", "kcp"},
		{"UDP + QUIC", "encrypted TLS 1.3 streams over UDP — self-tuning, great under loss", "quic"},
	}},
	{"WebSocket", "looks like normal web traffic — CDN friendly", []transportEntry{
		{"WS", "WebSocket — HTTP camouflage, CDN friendly", "ws"},
		{"WS Mux", "WebSocket — multiplexed", "wsmux"},
		{"WSS", "secure WebSocket — TLS encrypted", "wss"},
		{"WSS Mux", "TLS WebSocket — multiplexed", "wssmux"},
	}},
	{"Experimental", "newer ideas, still being proven — not for production yet", []transportEntry{
		{"xDi (ICMP)", "tunnels inside ping packets, for networks that filter UDP/TCP but not ICMP — Linux, needs root", "xdi"},
		{"IP Spoofing", "forges the source of raw IP packets, for a path that filters on the real flow — Linux, needs root; prove it on your route", "spoof"},
	}},
}

// chooseTransport walks the family menu and then the variant menu. It returns
// an empty string when the user backs out at either level.
func chooseTransport() string {
	for {
		groupOpts := make([]tui.Option, len(transportGroups))
		for i, g := range transportGroups {
			groupOpts[i] = tui.Option{Title: g.label, Desc: g.desc}
		}
		gi := tui.ChooseOpt("Select transport family:", groupOpts)
		if gi < 0 {
			return ""
		}
		group := transportGroups[gi]

		entryOpts := make([]tui.Option, len(group.entries))
		for i, e := range group.entries {
			entryOpts[i] = tui.Option{Title: e.label, Desc: e.desc}
		}
		ei := tui.ChooseOpt("Select "+group.label+" transport:", entryOpts)
		if ei < 0 {
			// Back to the family list rather than out of setup entirely.
			continue
		}
		if group.entries[ei].value == "" {
			tui.Warn(group.entries[ei].label + " is not available yet — please pick another transport.")
			tui.PressEnter()
			continue
		}
		return group.entries[ei].value
	}
}

// choosePreset asks for the performance profile. Turbo is preselected because
// it reproduces exactly what earlier versions called "Best Performance".
// The transport decides which profiles are on offer: Throughput only means
// something where this process runs the congestion control itself.
func choosePreset(transport string) string {
	options := presetOptionsFor(transport)
	opts := make([]tui.Option, len(options))
	for i, o := range options {
		opts[i] = tui.Option{Title: o.label, Desc: o.desc}
	}
	idx := tui.ChooseOpt("Performance preset:", opts)
	if idx < 0 {
		return PresetTurbo
	}
	return options[idx].value
}

// applyManualTuning asks the advanced questions for users who want to override
// the preset. It runs after ApplyPreset, so every prompt starts from the
// preset's value and anything left untouched keeps that value.
func applyManualTuning(s *TunnelSpec) {
	s.Nodelay = tui.Confirm("Enable TCP_NODELAY (lower latency)", s.Nodelay)
	s.KeepAlive = tui.PromptInt("Keepalive period (seconds)", s.KeepAlive)
	s.Heartbeat = tui.PromptInt("Heartbeat interval (seconds, 0 to disable)", s.Heartbeat)
	s.LogLevel = tui.PromptDefault("Log level (info/debug/warn/error)", s.LogLevel)
	// JSON is for feeding a log collector or a script; a person reading
	// journalctl is better served by the default text format.
	if tui.Confirm("Write logs as JSON (for log collectors and scripts)", s.LogFormat == "json") {
		s.LogFormat = "json"
	} else {
		s.LogFormat = ""
	}
	if s.Role == "server" {
		s.ChannelSize = tui.PromptInt("Channel size", s.ChannelSize)
		// UDP forwarding is not asked here: it is asked in the main setup flow,
		// next to the exposed ports it describes. Burying it under the advanced
		// settings — which default to "no" — meant a fresh install never saw
		// the question at all, and an Xray or WireGuard inbound came up with
		// nothing explaining why only half of it worked.
	} else {
		s.ConnectionPool = tui.PromptInt("Connection pool size", s.ConnectionPool)
		s.AggressivePool = tui.Confirm("Enable aggressive pool", s.AggressivePool)
	}
	// The MSS clamp is deliberately not part of any preset: it describes the
	// path the tunnel crosses, not how hard the tunnel is being pushed. There is
	// nothing to guess at either — Diagnose measures the path and prints the
	// number — so it stays at 0 until something has measured it.
	if !isDatagram(s.Transport) {
		tui.Warn("MSS caps the largest TCP segment the tunnel sends. Keep it at 0")
		tui.Warn("unless Diagnose reports that the path cannot carry full-sized")
		tui.Warn("packets — it prints the value, and both ends need the same one.")
		s.MSS = tui.PromptInt("TCP MSS clamp (bytes, 0 = automatic)", s.MSS)
	}
	if isMux(s.Transport) {
		s.MuxCon = tui.PromptInt("Mux connections/sessions", s.MuxCon)
		s.MuxVersion = tui.PromptInt("Mux version (1 or 2)", s.MuxVersion)
		s.MuxFrameSize = tui.PromptInt("Mux frame size", s.MuxFrameSize)
		s.MuxRecvBuffer = tui.PromptInt("Mux receive buffer", s.MuxRecvBuffer)
		s.MuxStreamBuffer = tui.PromptInt("Mux stream buffer", s.MuxStreamBuffer)
	}
	if isKCP(s.Transport) {
		s.KCPMTU = tui.PromptInt("KCP MTU (bytes, keep below the path MTU)", s.KCPMTU)
		s.KCPInterval = tui.PromptInt("KCP interval (ms — lower reacts faster, costs CPU)", s.KCPInterval)
		s.KCPSndWnd = tui.PromptInt("KCP send window (packets)", s.KCPSndWnd)
		s.KCPRcvWnd = tui.PromptInt("KCP receive window (packets)", s.KCPRcvWnd)
		s.KCPDataShards = tui.PromptInt("FEC data shards (0 disables error correction)", s.KCPDataShards)
		s.KCPParityShards = tui.PromptInt("FEC parity shards (losses repaired per group)", s.KCPParityShards)
	}
	// Zero-copy forwarding, offered only where it can actually engage: the
	// kernel path needs two plain TCP sockets, so a mux, websocket or datagram
	// transport would take the setting and quietly ignore it.
	if s.Transport == "tcp" {
		tui.Warn("Zero-copy hands forwarded traffic straight to the kernel. It is the")
		tui.Warn("fastest path and the least proven one — try it on a spare tunnel")
		tui.Warn("before a busy one. Nothing about it reaches the wire, so the two")
		tui.Warn("ends need not agree.")
		s.ZeroCopy = tui.Confirm("Enable zero-copy forwarding (experimental)", s.ZeroCopy)
	}

	// Manual edits no longer match any preset, so the tunnel is marked custom
	// and a later preset change will not silently overwrite these answers.
	s.Preset = ""
}

// setupServerTLS collects the certificate for wss/wssmux servers. Returns false
// if setup should be aborted.
//
// Three ways to get one, offered here rather than only under Edit so a tunnel
// that wants a real certificate is finished in one pass instead of being built
// and then reconfigured.
func setupServerTLS(s *TunnelSpec) bool {
	tui.Info("WSS transports need a TLS certificate.")
	fmt.Println()
	tui.Warn("Self-signed encrypts exactly as well — the client is Backpack's own")
	tui.Warn("code and does not verify it. A real certificate matters for how the")
	tui.Warn("connection looks from outside: real HTTPS on 443 is never self-signed,")
	tui.Warn("so a self-signed one stands out. It is also what a CDN requires.")
	fmt.Println()

	choice := tui.ChooseOpt("TLS certificate:", []tui.Option{
		{Title: "Self-signed, generated now", Desc: "works anywhere, including on a bare IP — the default"},
		{Title: "Let's Encrypt, automatic", Desc: "free and real — needs a domain pointing at this server"},
		{Title: "Use existing certificate/key files", Desc: "a certificate you already have on disk"},
	})

	switch choice {
	case 0:
		host := strings.TrimSpace(tui.PromptDefault("Domain or IP to embed in the cert (optional)", ""))
		return generateSelfSigned(s, host)

	case 1:
		// The tunnel port is already chosen at this point, so it can be
		// checked rather than described in the abstract. On 443 validation
		// happens over the tunnel's own listener and nothing else is needed;
		// anywhere else it falls back to port 80.
		if p := addrPort(s.BindAddr); p != "443" {
			tui.Warn("This tunnel is on port " + p + ", not 443.")
			tui.Warn("Let's Encrypt will have to validate over port 80 instead, so")
			tui.Warn("port 80 must be free on this server and open in the firewall.")
			fmt.Println()
		}

		domain, email, ok := promptACMEDomain("", "")
		if !ok {
			return false
		}
		s.ACMEDomain, s.ACMEEmail = domain, email
		// The self-signed pair is generated anyway. It is what the config still
		// points at, and it is the fallback if issuance fails — without it a
		// failed ACME request would leave the tunnel with no certificate at all.
		if !generateSelfSigned(s, domain) {
			return false
		}
		fmt.Println()
		tui.Success("Let's Encrypt will be used for " + domain + ".")
		tui.Warn("The certificate is requested on the first connection. If it does")
		tui.Warn("not arrive, the tunnel keeps working on the self-signed one —")
		tui.Warn("check: journalctl -u " + app.ServiceName(s.Name) + " -n 50")
		return true

	case 2:
		s.TLSCert = strings.TrimSpace(tui.Prompt("Path to TLS certificate (e.g. /etc/letsencrypt/live/x/fullchain.pem): "))
		s.TLSKey = strings.TrimSpace(tui.Prompt("Path to TLS key (e.g. /etc/letsencrypt/live/x/privkey.pem): "))
		if err := validCertPair(s.TLSCert, s.TLSKey); err != nil {
			tui.Error("Invalid certificate: " + err.Error())
			tui.PressEnter()
			return false
		}
		return true

	default:
		return false
	}
}

// generateSelfSigned creates the self-signed pair and records it on the spec.
func generateSelfSigned(s *TunnelSpec, host string) bool {
	cert, key, err := EnsureSelfSignedCert(s.Name, host)
	if err != nil {
		tui.Error("Certificate generation failed: " + err.Error())
		tui.PressEnter()
		return false
	}
	s.TLSCert, s.TLSKey = cert, key
	return true
}

// promptACMEDomain asks for the domain and email for a Let's Encrypt
// certificate, checking that the domain actually points here.
//
// The check happens before anything is saved. Issuance is validated by Let's
// Encrypt connecting to the domain, so a typo or a missing DNS record means it
// silently never gets a certificate — much better to say so now than to let the
// tunnel restart and leave the user wondering why nothing changed. Shared with
// Edit → Certificate so both paths warn about the same things.
func promptACMEDomain(currentDomain, currentEmail string) (domain, email string, ok bool) {
	fmt.Println()
	tui.Warn("Requirements, all of them:")
	tui.Warn("  • a domain whose A record points at this server's IP")
	tui.Warn("  • port 80 reachable from outside, OR this tunnel on port 443")
	tui.Warn("  • this server able to reach acme-v02.api.letsencrypt.org")
	fmt.Println()

	domain = strings.TrimSpace(tui.PromptDefault("Domain", currentDomain))
	if domain == "" {
		tui.Error("A domain is required.")
		tui.PressEnter()
		return "", "", false
	}
	if net.ParseIP(domain) != nil {
		tui.Error("That is an IP address. Let's Encrypt only issues for domain names.")
		tui.PressEnter()
		return "", "", false
	}

	if ips, err := net.LookupHost(domain); err != nil {
		tui.Error("That domain does not resolve: " + err.Error())
		if !tui.Confirm("Use it anyway", false) {
			return "", "", false
		}
	} else {
		tui.Info("Resolves to: " + strings.Join(ips, ", "))
		if mine := PublicIPv4(); mine != "" && mine != "-" && !contains(ips, mine) {
			tui.Error("None of those is this server's address (" + mine + ").")
			tui.Warn("Let's Encrypt validates by connecting to the domain, so it would")
			tui.Warn("reach a different machine and issuance would fail.")
			if !tui.Confirm("Use it anyway", false) {
				return "", "", false
			}
		}
	}

	email = strings.TrimSpace(tui.PromptDefault("Email for expiry warnings (optional)", currentEmail))
	return domain, email, true
}

// askPck collects the packet-level TCP carrier's settings, and is a no-op for
// every other transport.
//
// There is deliberately almost nothing to collect. paqet, which this transport
// takes its approach from, asks for the interface, the local address and the
// gateway's MAC and devotes a page of its README to finding each; all three are
// already in the routing and neighbour tables, so they are read rather than
// asked for. What is left is one genuine choice — what the flags on the wire
// look like — and an escape hatch for the host where the lookup guesses wrong.
func askPck(s *TunnelSpec) {
	if s.Transport != "pck" {
		return
	}
	fmt.Println()
	tui.Warn("TCP + PCK builds and reads its own TCP packets instead of using the")
	tui.Warn("kernel's TCP stack. Nothing is forged — the address and ports are")
	tui.Warn("real — but no socket, no handshake and no connection state exist, so")
	tui.Warn("connection tracking and netfilter have nothing to act on.")
	tui.Warn("Linux only, needs root, and BOTH ends must be on this transport.")
	fmt.Println()
	tui.Info("The interface, local address and next hop are read from this machine's")
	tui.Info("own routing table — there is nothing to enter for them.")
	fmt.Println()

	// The default is what bulk data carries, and it is right until a specific
	// path is known to match on it, so the question leads with that.
	tui.Info("TCP flags stamped on the tunnel's packets. Vary them only if the path")
	tui.Info("is known to match on the pattern; each end decides its own.")
	opts := network.SuggestedTCPFlagCycles()
	menu := make([]tui.Option, len(opts))
	for i, o := range opts {
		menu[i] = tui.Option{Title: o.Value, Desc: o.Desc}
	}
	if i := tui.ChooseOpt("Flag pattern:", menu); i > 0 {
		s.PckFlags = strings.Split(opts[i].Value, ",")
	} else {
		s.PckFlags = nil // the default, left out of the config entirely
	}

	fmt.Println()
	if tui.Confirm("Override the automatic interface / gateway detection", false) {
		tui.Warn("Leave either empty to keep the automatic answer for it.")
		if names := routableInterfaces(); len(names) > 0 {
			tui.Info("Interfaces: " + strings.Join(names, ", "))
		}
		for {
			raw := strings.TrimSpace(tui.PromptDefault("Interface", ""))
			if raw == "" {
				break
			}
			if _, err := net.InterfaceByName(raw); err != nil {
				tui.Error(fmt.Sprintf("no such interface: %v", err))
				continue
			}
			s.PckInterface = raw
			break
		}
		for {
			raw := strings.TrimSpace(tui.PromptDefault("Gateway MAC", ""))
			if raw == "" {
				break
			}
			if _, err := net.ParseMAC(raw); err != nil {
				tui.Error(fmt.Sprintf("not a MAC address: %v", err))
				continue
			}
			s.PckGatewayMAC = raw
			break
		}
	}
}

// askSpoof collects the IP-spoofing carrier's settings. It runs on both ends
// for the spoof transport and is a no-op for every other transport.
//
// The forged source is what the far end and the network see; whether replies
// find their way back is a property of the route, which is why the wizard says
// out loud that it must be proven — with the spoof tester (Manage → IP Spoofing
// tester).
func askSpoof(s *TunnelSpec) {
	if s.Transport != "spoof" {
		return
	}
	// The other side of this transport is a person who picked it off a menu
	// because it sounded like the one that gets through, and who has never seen
	// a forged packet. So the screen explains what the thing is, what it needs
	// from the network, and which single answer to give when unsure — and the
	// questions that only a tuned setup needs are behind a confirm that defaults
	// to no, rather than in the way.
	here, there := "kharej", "Iran"
	if s.Role == "server" {
		here, there = "Iran", "kharej"
	}

	fmt.Println()
	tui.Title("IP Spoofing")
	fmt.Println()
	tui.Info("This carrier writes its own IP packets and stamps a FAKE source address")
	tui.Info("on them, so what leaves this machine does not look like it came from")
	tui.Info("here. It is for a path that blocks or throttles by address.")
	fmt.Println()
	tui.Warn("It is experimental, and it only carries anything where the network")
	tui.Warn("above this machine forwards packets with a forged source. Plenty of")
	tui.Warn("providers drop them, and there is no way to tell from here.")
	tui.Warn("Manage → IP Spoofing Tester tries a list and says which ones arrived.")
	fmt.Println()
	tui.Info("Both ends must be set up to match. You will answer the same questions")
	tui.Info("on the " + there + " server, with the two addresses the other way round.")
	fmt.Println()
	tui.Warn("Not sure yet? Take the recommended answer at every step — press Enter")
	tui.Warn("throughout and you get a working, unforged tunnel. Nothing here is")
	tui.Warn("final: Manage → Edit → IP Spoofing changes any of it afterwards, and")
	tui.Warn("so does the web panel, so come back once the tester has found a")
	tui.Warn("source that passes.")

	step := stepper(4)

	// ---- 1. what the packets look like -------------------------------------
	// One question with a recommended answer, and the full menu only for
	// somebody who already knows they want something else.
	step("What the packets look like on the wire")
	tui.Info("The forged packets can be dressed as UDP, as ping (ICMP), or as a TCP")
	tui.Info("flow. UDP is the plainest and passes nearly everywhere; the other two")
	tui.Info("are for a path that filters UDP specifically.")
	tui.Warn("Whatever you pick here, the " + there + " end must pick the same one.")
	fmt.Println()
	if tui.Confirm("Use UDP — the recommended profile", true) {
		s.SpoofProfile = "udp"
	} else {
		s.SpoofProfile = askSpoofProfile("Packet profile:")
	}

	fmt.Println()
	tui.Info("A path can filter one direction differently from the other. If it does,")
	tui.Info("each direction can wear its own profile — uplink is kharej → Iran,")
	tui.Info("downlink is Iran → kharej. Almost no path needs this.")
	if tui.Confirm("Set the two directions separately", false) {
		s.SpoofUplink = askSpoofProfile("Uplink profile (kharej → Iran):")
		s.SpoofDownlink = askSpoofProfile("Downlink profile (Iran → kharej):")
	}

	// ---- 2. where the replies go -------------------------------------------
	// Server only, and not optional: the forged packets do not carry the
	// client's address, so without this the server has nowhere to answer.
	step("Where this end sends its replies")
	if s.Role == "server" {
		tui.Info("The " + there + " machine forges its source address, so its packets do not")
		tui.Info("say where they came from. This server has to be told, or it has")
		tui.Info("nowhere to send the answers.")
		fmt.Println()
		tui.Warn("Enter the REAL public IPv4 of the " + there + " server — the address you")
		tui.Warn("SSH into it with, not a forged one.")
		for {
			raw := strings.TrimSpace(tui.Prompt("Real IPv4 of the " + there + " server: "))
			if net.ParseIP(raw).To4() != nil {
				s.SpoofPeerIP = raw
				break
			}
			tui.Error("That is not an IPv4 address. It looks like 203.0.113.10")
		}
	} else {
		tui.Info("Nothing to answer here: this end dialled the " + there + " server, so it")
		tui.Info("already knows the address to send to. The " + there + " side is the one")
		tui.Info("that has to be told yours.")
	}

	// ---- 3. the forged source ----------------------------------------------
	step("The address to forge")
	tui.Info("This is the address stamped on the packets this machine sends.")
	fmt.Println()
	tui.Warn("Leave it empty and nothing is forged: the tunnel comes up on this")
	tui.Warn("machine's real address and works normally. That is the right answer")
	tui.Warn("for a first run — get the tunnel up, run the tester, then set an")
	tui.Warn("address that passed from Manage → Edit → IP Spoofing.")
	fmt.Println()
	tui.Info("Several addresses, separated by commas, are rotated through one per")
	tui.Info("session — that is what gets past a limit or a block that counts by")
	tui.Info("address.")
	raw := strings.TrimSpace(tui.PromptDefault("Forged source IPv4 (empty = do not forge)", ""))
	if raw != "" {
		var pool []string
		for _, part := range strings.Split(raw, ",") {
			ip := strings.TrimSpace(part)
			if ip == "" {
				continue
			}
			if net.ParseIP(ip).To4() == nil {
				tui.Warn(fmt.Sprintf("skipping %q — not an IPv4 address", ip))
				continue
			}
			pool = append(pool, ip)
		}
		if len(pool) == 1 {
			s.SpoofSrcIP = pool[0]
		} else if len(pool) > 1 {
			s.SpoofSrcIP = pool[0]
			s.SpoofSrcPool = pool
		}
	}

	// Only worth asking on a machine that has somewhere else to go. On the
	// single-uplink VPS this transport usually runs on, the answer is "the one
	// route there is", and a prompt for it is a prompt to get wrong.
	if names := routableInterfaces(); len(names) > 1 {
		fmt.Println()
		tui.Info("This machine has more than one network interface, so the raw packets")
		tui.Info("can be pinned to one of them.")
		tui.Warn("Available: " + strings.Join(names, ", ") + " — leave empty to let the")
		tui.Warn("kernel pick, which is right unless you know it picks wrong.")
		for {
			iface := strings.TrimSpace(tui.PromptDefault("Interface", ""))
			if iface == "" {
				break
			}
			if _, err := net.InterfaceByName(iface); err != nil {
				tui.Error(fmt.Sprintf("no such interface: %v", err))
				continue
			}
			s.SpoofInterface = iface
			break
		}
	}

	// ---- 4. Relay mode ------------------------------------------------------
	step("Relay mode, instead of a KCP tunnel over forwarded ports")
	tui.Info("Normally this tunnel wraps a reliable KCP tunnel over the forwarded")
	tui.Info("ports. Relay mode does something else: it strips KCP and runs a bare")
	tui.Info("datagram relay to a local UDP target, so you carry something that")
	tui.Info("brings its own reliability — a whole WireGuard VPN, or another tunnel.")
	tui.Info("The forwarded ports are ignored in this mode.")
	tui.Warn("Say no unless you are already running WireGuard (or similar) and want it tunnelled.")
	fmt.Println()
	if tui.Confirm("Enable relay mode", false) {
		s.SpoofPipe = true
		def := "127.0.0.1:51820"
		fmt.Println()
		if s.Role == "server" {
			tui.Info("Where the inner service listens on THIS server (e.g. the real")
			tui.Info("WireGuard) — datagrams coming out of the tunnel are handed to it there.")
		} else {
			tui.Info("Where the tunnel should listen on THIS machine — point the inner")
			tui.Info("app's endpoint (e.g. WireGuard's `endpoint`) at exactly this address.")
		}
		s.SpoofPipeAddr = strings.TrimSpace(tui.PromptDefault("Local UDP endpoint", def))
		if s.SpoofPipeAddr == "" {
			s.SpoofPipeAddr = def
		}
	}

	spoofSummary(s, here, there)
}

// stepper returns a function that prints numbered section headings, so the
// screen says how far through it is rather than scrolling past as one wall.
func stepper(total int) func(title string) {
	n := 0
	return func(title string) {
		n++
		fmt.Println()
		tui.Rule()
		tui.Success(fmt.Sprintf("Step %d of %d — %s", n, total, title))
		fmt.Println()
	}
}

// spoofSummary repeats the answers back and says what has to be true on the
// other server for them to work. Everything in a spoof setup is paired, and the
// pairing is the part that goes wrong.
func spoofSummary(s *TunnelSpec, here, there string) {
	fmt.Println()
	tui.Rule()
	tui.Success("IP Spoofing — what this " + here + " end will do")
	fmt.Println()

	profile := s.SpoofProfile
	if s.SpoofUplink != "" || s.SpoofDownlink != "" {
		up, down := s.SpoofUplink, s.SpoofDownlink
		if up == "" {
			up = profile
		}
		if down == "" {
			down = profile
		}
		profile = fmt.Sprintf("uplink %s, downlink %s", up, down)
	}
	tui.Info("Packets look like : " + profile)

	switch {
	case len(s.SpoofSrcPool) > 1:
		tui.Info("Forged source     : " + strings.Join(s.SpoofSrcPool, ", ") + " (one per session)")
	case s.SpoofSrcIP != "":
		tui.Info("Forged source     : " + s.SpoofSrcIP)
	default:
		tui.Info("Forged source     : none — this machine's real address")
	}
	if s.SpoofPeerIP != "" {
		tui.Info("Replies go to     : " + s.SpoofPeerIP + " (the " + there + " server)")
	}
	if s.SpoofInterface != "" {
		tui.Info("Leaves by         : " + s.SpoofInterface)
	}
	if s.SpoofPipe {
		tui.Info("Relay mode        : on, forwarding to " + s.SpoofPipeAddr + " (KCP off, forwarded ports ignored)")
	}

	fmt.Println()
	tui.Warn("On the " + there + " server: the same packet profile, and if you forged a")
	tui.Warn("source here, tell that end to expect it.")
	if s.Role == "client" {
		tui.Warn("That end also needs THIS machine's real public IPv4, or it has")
		tui.Warn("nowhere to send its replies.")
	}
	tui.Warn("Nothing here is proven until traffic actually crosses — if the tunnel")
	tui.Warn("comes up but carries nothing, the forged source is being dropped.")
	tui.Warn("Manage → IP Spoofing Tester finds one that is not.")
}

// askSpoofProfile prompts for one packet profile and returns its config value.
func askSpoofProfile(title string) string {
	switch tui.ChooseOpt(title, []tui.Option{
		{Title: "UDP", Desc: "most compatible — plain datagrams (recommended)"},
		{Title: "ICMP", Desc: "looks like ping traffic — good where UDP is filtered"},
		{Title: "TCP", Desc: "looks like a TCP flow — auto-manages an iptables RST rule"},
	}) {
	case 1:
		return "icmp"
	case 2:
		return "tcp"
	default:
		return "udp"
	}
}

// askProxyProtocol offers to forward the real client IP to the service behind
// the tunnel. Without it that service sees every connection as coming from the
// tunnel itself, which is why per-user device limits in VPN panels stop working
// once traffic is tunnelled.
func askProxyProtocol(s *TunnelSpec) {
	if !supportsProxyProtocol(s.Transport) {
		return
	}
	fmt.Println()
	tui.Info("Send the real client IP to the service behind the tunnel?")
	tui.Warn("Without this, your panel sees every user coming from one address, so")
	tui.Warn("per-user device/IP limits cannot work. With it, each connection")
	tui.Warn("carries a PROXY protocol v2 header holding the real client IP.")
	fmt.Println()
	tui.Error("Only turn this on if the service is set to ACCEPT the PROXY protocol.")
	tui.Error("If it is not, it will read the header as data and every connection breaks.")
	tui.Warn("In X-UI / Marzban this is the inbound option named \"Accept Proxy Protocol\".")
	fmt.Println()
	s.ProxyProtocol = tui.Confirm("Enable PROXY protocol (send real client IP)", false)
}

// uniqueName ensures the chosen name is valid and not already taken.
func uniqueName(name string) string {
	for {
		switch {
		case !validName(name):
			tui.Warn(fmt.Sprintf("Invalid name %q — use letters, digits, dots, dashes (max 40).", name))
		case fileExists(app.ConfigPath(name)):
			tui.Warn(fmt.Sprintf("A tunnel named %q already exists.", name))
		default:
			return name
		}
		name = tui.Prompt("Choose a different name: ")
	}
}

// SetupServer runs the interactive server (edge/Iran) setup flow.
func SetupServer() {
	tui.Clear()
	tui.Title("Setup Server")
	tui.Warn("Iran side — reverse tunnel that exposes ports on this machine.")
	fmt.Println()

	transport := chooseTransport()
	if transport == "" {
		return
	}

	// AcceptUDP starts off: a forwarded port carries TCP only unless the
	// operator turns UDP on, which is asked for below. See
	// config.ServerConfig.ForwardsUDP.
	s := TunnelSpec{Role: "server", Transport: transport, AcceptUDP: false}

	port := tui.Prompt("Tunnel (control) port: ")
	if !validPort(port) {
		tui.Error("Invalid port.")
		tui.PressEnter()
		return
	}
	// Binding the IPv6 wildcard accepts IPv4 as well on a normal dual-stack
	// host, so this is "IPv6 too" rather than "IPv6 instead".
	bind := "0.0.0.0"
	if tui.Confirm("Listen on IPv6 as well", false) {
		bind = "::"
	}
	s.BindAddr = net.JoinHostPort(bind, port)

	defaultName := "server-" + port
	s.Name = uniqueName(tui.PromptDefault("Tunnel name", defaultName))

	suggested := randomToken(64)
	tui.Info("Suggested 64-char token (press Enter to accept — copy it to the client):")
	fmt.Println("  " + tui.Color(tui.Bold+tui.White, suggested))
	s.Token = tui.PromptDefault("Security token", suggested)

	// Spelled out because getting this wrong is the single most common way a
	// working tunnel looks broken: the tunnel comes up, carries the connection,
	// and then the far side has nothing to hand it to.
	fmt.Println()
	tui.Warn("A bare port (443) means: expose 443 here, and the KHAREJ server")
	tui.Warn("forwards it to its own 127.0.0.1:443 — so your panel must listen")
	tui.Warn("on that exact port there.")
	tui.Warn("If the service is elsewhere, say so: 443=127.0.0.1:2096")
	tui.Warn("Several backends for one port: 443=127.0.0.1:2096|127.0.0.1:2097")
	tui.Warn("(separated by |, checked continuously, balanced over the live ones)")
	fmt.Println()

	portsRaw := tui.Prompt("Exposed ports (comma separated, e.g. 443,8080 or 443=1.1.1.1:443): ")
	s.Ports = parsePorts(portsRaw)
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

	// Asked in the main flow, right after the ports it applies to, rather than
	// only under the advanced settings: it is off by default, and a tunnel
	// fronting an Xray or WireGuard inbound is then broken in a way nothing at
	// setup accounts for. The cost of saying yes is spelled out so a plain web
	// tunnel still says no — see config.ServerConfig.ForwardsUDP.
	fmt.Println()
	tui.Warn("UDP forwarding is OFF by default. Say yes for Xray/Shadowsocks UDP,")
	tui.Warn("WireGuard, DNS or games. Say no for a plain web or proxy tunnel: a")
	tui.Warn("browser's QUIC is UDP on 443, and tunnelling it crowds out the TCP")
	tui.Warn("forwards sharing this tunnel. It can be changed later under Edit.")
	s.AcceptUDP = tui.Confirm("Carry UDP as well as TCP on the exposed ports", false)

	showForwardTargets(s.Ports, s.AcceptUDP)

	if needsTLS(transport) && !setupServerTLS(&s) {
		return
	}
	askSimpleAuth(&s, transport)

	askSpoof(&s)

	askPck(&s)

	askProxyProtocol(&s)

	ApplyPreset(&s, choosePreset(s.Transport))
	if tui.Confirm("Fine-tune the advanced settings by hand", false) {
		applyManualTuning(&s)
	}

	finishSetup(s)
}

// SetupClient runs the interactive client (origin/kharej) setup flow.
func SetupClient() {
	tui.Clear()
	tui.Title("Setup Client")
	tui.Warn("Kharej side — reverse tunnel that dials out to the Iran server.")
	fmt.Println()

	transport := chooseTransport()
	if transport == "" {
		return
	}

	s := TunnelSpec{Role: "client", Transport: transport}

	remoteHost := tui.Prompt("Server address (IP or domain of the server): ")
	remotePort := tui.Prompt("Server tunnel port: ")
	if remoteHost == "" || !validPort(remotePort) {
		tui.Error("Invalid server address or port.")
		tui.PressEnter()
		return
	}
	// JoinHostPort adds the brackets an IPv6 literal needs, and leaves a
	// hostname or IPv4 address alone.
	s.RemoteAddr = net.JoinHostPort(strings.Trim(remoteHost, "[]"), remotePort)

	if !checkServerAddress(strings.Trim(remoteHost, "[]"), transport, remotePort) {
		return
	}

	defaultName := "client-" + remotePort
	s.Name = uniqueName(tui.PromptDefault("Tunnel name", defaultName))

	tui.Info("Enter the SAME token you configured on the server.")
	s.Token = tui.PromptDefault("Security token", "backpack")

	if isWS(transport) {
		tui.Info("Optional edge IP: connect to a CDN edge (e.g. Cloudflare) instead of")
		tui.Info("resolving the server address directly. Leave empty to skip.")
		s.EdgeIP = strings.TrimSpace(tui.PromptDefault("Edge IP", ""))
	}
	askSimpleAuth(&s, transport)

	askSpoof(&s)

	askPck(&s)

	// Proxy, interface pinning, and backup addresses are connectivity options
	// that most tunnels never need. Gate them behind one confirm so the common
	// path stays short, and only print their explanatory text on demand.
	fmt.Println()
	if tui.Confirm("Configure optional connection settings (proxy, interface, backup addresses)", false) {
		// Only offered where it can actually work: the datagram transports carry
		// their data in UDP, which a TCP proxy cannot relay.
		if !isDatagram(transport) {
			fmt.Println()
			tui.Info("Optional proxy: reach the tunnel server through a SOCKS5 or HTTP proxy,")
			tui.Info("for a machine that cannot open outbound connections directly.")
			tui.Warn("e.g. socks5://127.0.0.1:1080 — leave empty to dial the server directly.")
			for {
				raw := strings.TrimSpace(tui.PromptDefault("Proxy URL", ""))
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

			// Only worth asking on a machine that has somewhere else to go. On a
			// single-uplink server the answer is always "the one route there is",
			// and a prompt for it is a prompt to get wrong.
			if names := routableInterfaces(); len(names) > 1 {
				fmt.Println()
				tui.Info("This machine has more than one network interface. You can pin the")
				tui.Info("tunnel to one of them, or to a source address, if it should not")
				tui.Info("leave by whichever route the kernel picks.")
				tui.Warn("Available: " + strings.Join(names, ", ") + " — leave empty to let the kernel decide.")
				for {
					raw := strings.TrimSpace(tui.PromptDefault("Interface", ""))
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
				s.LocalAddr = strings.TrimSpace(tui.PromptDefault("Source address (optional)", ""))
			}
		}

		// Backup addresses make the tunnel survive a filtered server IP: the
		// client tries each one in turn until something answers.
		fmt.Println()
		tui.Info("Optional backup server addresses — if the main address ever stops")
		tui.Info("answering, the client fails over to these automatically.")
		tui.Warn("Comma separated; a bare IP reuses the main port. Leave empty to skip.")
		if raw := strings.TrimSpace(tui.PromptDefault("Backup addresses", "")); raw != "" {
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
			fmt.Println()
			tui.Info("Automatic failover scores every address (latency, jitter, loss) and")
			tui.Info("keeps traffic on the healthiest one — the multi-exit gaming setup.")
			tui.Warn("Only turn this on if every address reaches the SAME server — a")
			tui.Warn("second IP of it, another port, or a CDN edge in front of it.")
			if tui.Confirm("Enable automatic failover to the healthiest server", false) {
				s.HealthFailover = true
			} else {
				fmt.Println()
				tui.Info("Load balancing instead spreads connections over ALL those addresses")
				tui.Info("at once, rather than picking the single best one.")
				s.LoadBalance = tui.Confirm("Enable load balancing", false)
			}
		}
	}

	ApplyPreset(&s, choosePreset(s.Transport))
	if tui.Confirm("Fine-tune the advanced settings by hand", false) {
		applyManualTuning(&s)
	}

	finishSetup(s)
}

// finishSetup persists the tunnel, applies system-level tuning, and reports
// the result.
func finishSetup(s TunnelSpec) {
	tui.Info("Applying system network optimizations...")
	optimize.ApplyQuiet()

	service, err := s.Save()
	if err != nil {
		tui.Error("Failed to create tunnel: " + err.Error())
		tui.PressEnter()
		return
	}

	fmt.Println()
	if IsActive(service) {
		tui.Success(fmt.Sprintf("Tunnel %q is up and running (%s).", s.Name, service))
	} else {
		tui.Warn(fmt.Sprintf("Tunnel %q created but not active yet — check logs.", s.Name))
	}
	tui.PressEnter()
}

// showForwardTargets spells out, for each mapping, what the kharej server will
// be expected to have listening.
//
// The mapping is entered on the Iran server but describes something on the
// other machine, and that indirection is where people go wrong. Printing the
// resolved target turns "443" into a concrete instruction they can go and
// check, before the tunnel is built rather than after it appears broken.
//
// acceptUDP decides what the firewall advice says: a rule opened for TCP is not
// opened for UDP, and telling someone to open a UDP port on a tunnel that
// forwards only TCP reads as a promise the tunnel does not keep.
func showForwardTargets(ports []string, acceptUDP bool) {
	type target struct{ exposed, dest string }
	var targets []target

	for _, p := range ports {
		p = strings.TrimSpace(p)
		exposed, dest, found := strings.Cut(p, "=")
		exposed = strings.TrimSpace(exposed)
		if !found {
			// A bare port, or a bare range: the far side dials the same port on
			// its own loopback.
			dest = "127.0.0.1:" + exposed
		} else {
			// A destination may name several backends separated by "|", so
			// resolve each one; otherwise a list would be shown as a single
			// nonsense address.
			var parts []string
			for _, d := range strings.Split(strings.TrimSpace(dest), "|") {
				d = strings.TrimSpace(d)
				if d == "" {
					continue
				}
				// A destination given as just a port means loopback there too.
				if !strings.Contains(d, ":") {
					d = "127.0.0.1:" + d
				}
				parts = append(parts, d)
			}
			dest = strings.Join(parts, "  |  ")
		}
		targets = append(targets, target{exposed, dest})
	}
	if len(targets) == 0 {
		return
	}

	fmt.Println()
	tui.Info("On the KHAREJ server, these must be listening:")
	for _, t := range targets {
		fmt.Printf("  %s%s%s  →  %s%s%s\n",
			tui.Gray, t.exposed, tui.Reset,
			tui.Bold+tui.White, t.dest, tui.Reset)
	}
	fmt.Println()
	tui.Warn("Check there with:  ss -tlnp | grep <port>")
	tui.Warn("A panel bound to a public IP instead of 127.0.0.1 will refuse the")
	tui.Warn("connection — in that case map it explicitly: 443=<that IP>:443")
	fmt.Println()
	// A firewall opened for TCP is not opened for UDP, which is the thing
	// people miss — so say which one this tunnel actually needs.
	if acceptUDP {
		tui.Info("These ports carry UDP as well as TCP (Xray, Shadowsocks, DNS, games).")
		tui.Warn("Open BOTH in the firewall here:  ufw allow <port>/tcp && ufw allow <port>/udp")
	} else {
		tui.Info("These ports carry TCP only — UDP forwarding is off for this tunnel.")
		tui.Warn("Open them in the firewall here:  ufw allow <port>/tcp")
		tui.Warn("If you later need UDP, turn it on under Manage -> Edit -> Forward UDP")
		tui.Warn("and open <port>/udp as well — opening the UDP port alone does nothing.")
	}
	fmt.Println()
}

// checkServerAddress resolves a domain and reports what it points at, returning
// false if the user decides to start over.
//
// A domain is fine as long as it resolves straight to the server. What is not
// fine is a domain proxied through a CDN: the client then connects to the CDN,
// which relays only what it chooses to. For a raw TCP or KCP tunnel that means
// it never works — and the symptom arrives much later as an HTTP error page
// where the protocol expected its own bytes, which is close to impossible to
// trace back to a DNS record.
//
// WebSocket through a CDN is the one combination that does work, and only on a
// port the CDN proxies, so that case is called out separately rather than
// warned about in general.
func checkServerAddress(host, transport, port string) bool {
	if host == "" || net.ParseIP(host) != nil {
		return true // an IP address needs no explanation
	}

	ips, err := net.LookupHost(host)
	if err != nil {
		tui.Error("That domain does not resolve: " + err.Error())
		return tui.Confirm("Use it anyway", false)
	}

	v4, v6 := splitFamilies(ips)

	fmt.Println()
	if len(v4) > 0 {
		tui.Info(host + " → IPv4: " + strings.Join(v4, ", "))
	}
	if len(v6) > 0 {
		tui.Info(host + " → IPv6: " + strings.Join(v6, ", "))
	}

	cdn := detectCDN(ips)
	if cdn == "" {
		// An AAAA record alongside an A record is a quiet trap. Resolving a
		// name yields one address, and it may be the IPv6 one — so the tunnel
		// connects over IPv6 even though everything was set up and tested over
		// IPv4. If IPv6 routing between the two servers is broken, or the
		// firewall only opens the port for IPv4, it fails with a name and works
		// with a bare address, which looks like the name being at fault.
		if len(v6) > 0 && len(v4) > 0 {
			tui.Error("This domain has both IPv4 and IPv6 addresses.")
			tui.Warn("The tunnel may connect over IPv6, which only works if IPv6 reaches")
			tui.Warn("the server AND the port is open for it. If a bare IP works and this")
			tui.Warn("domain does not, that is almost certainly why.")
			tui.Warn("Fix it by removing the AAAA record, or use the IPv4 address here.")
			fmt.Println()
			return tui.Confirm("Continue with this address", false)
		}
		tui.Warn("Make sure that is this server's peer — the machine running the")
		tui.Warn("server side of the tunnel. If it is not, nothing will connect.")
		fmt.Println()
		return tui.Confirm("Continue with this address", true)
	}

	// Proxied. Whether that can work depends entirely on the transport.
	tui.Error("That address belongs to " + cdn + ", not to a server.")
	fmt.Println()
	if isWS(transport) && cdnPort(port) {
		tui.Warn("A WebSocket tunnel can go through a CDN, and " + port + " is a port")
		tui.Warn(cdn + " proxies — so this combination can work.")
		tui.Warn("The server side needs a certificate the CDN accepts: use")
		tui.Warn("Let's Encrypt there, or set the CDN's SSL mode to Flexible.")
	} else {
		tui.Error("This will not work.")
		tui.Warn("A CDN relays web traffic, not a raw tunnel. Either:")
		tui.Warn("  • set the DNS record to DNS-only (grey cloud), or")
		tui.Warn("  • use the server's IP address directly, or")
		tui.Warn("  • switch to WSS on port 443, which a CDN does relay")
	}
	fmt.Println()
	return tui.Confirm("Continue anyway", false)
}

// cloudflareRanges are Cloudflare's published IPv4 networks.
//
// An address list rather than a reverse lookup, because reverse DNS does not
// work for this: Cloudflare's addresses have no PTR record naming Cloudflare,
// so a name-based check silently never fires — which is worse than no check,
// since it reads as "not a CDN" and gives false confidence.
//
// These ranges change very rarely. If one is missed, the result is the old
// behaviour — a general warning rather than a specific one — never a wrong
// answer.
var cloudflareRanges = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
}

// otherCDNNames are matched against reverse DNS, which does work for some
// providers even though it does not for Cloudflare.
var otherCDNNames = map[string]string{
	"cloudfront": "CloudFront",
	"akamai":     "Akamai",
	"fastly":     "Fastly",
	"gcore":      "Gcore",
	"arvancloud": "ArvanCloud",
	"derak":      "Derak Cloud",
}

// detectCDN names the CDN an address belongs to, or "" if it looks like an
// ordinary server.
func detectCDN(ips []string) string {
	for _, raw := range ips {
		ip := net.ParseIP(raw)
		if ip == nil {
			continue
		}
		for _, cidr := range cloudflareRanges {
			_, network, err := net.ParseCIDR(cidr)
			if err == nil && network.Contains(ip) {
				return "Cloudflare"
			}
		}
	}
	for _, raw := range ips {
		names, err := net.LookupAddr(raw)
		if err != nil {
			continue
		}
		for _, n := range names {
			n = strings.ToLower(n)
			for needle, label := range otherCDNNames {
				if strings.Contains(n, needle) {
					return label
				}
			}
		}
	}
	return ""
}

// cdnPort reports whether a CDN would proxy this port at all. These are the
// ports Cloudflare relays; the other providers overlap closely enough.
func cdnPort(port string) bool {
	switch port {
	case "443", "2053", "2083", "2087", "2096", "8443",
		"80", "8080", "8880", "2052", "2082", "2086", "2095":
		return true
	}
	return false
}

// splitFamilies separates resolved addresses into IPv4 and IPv6.
func splitFamilies(ips []string) (v4, v6 []string) {
	for _, raw := range ips {
		ip := net.ParseIP(raw)
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			v4 = append(v4, raw)
		} else {
			v6 = append(v6, raw)
		}
	}
	return v4, v6
}

// routableInterfaces lists the up, non-loopback interfaces that hold an
// address — the ones a tunnel could plausibly be pinned to.
//
// Loopback and down interfaces are left out because offering them would only
// invite an answer that cannot work, and an interface with no address of its
// own is not somewhere traffic can leave by.
func routableInterfaces() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var names []string
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil || len(addrs) == 0 {
			continue
		}
		names = append(names, ifi.Name)
	}
	return names
}

// askSimpleAuth offers the raw-token authorisation that a wss tunnel behind a
// TLS-terminating proxy needs. It is only meaningful there: over plain ws the
// token already goes raw, and the datagram and TCP transports have no TLS
// binding to turn off. Off by default, because without such a proxy it hands
// the token to whoever terminates the TLS.
func askSimpleAuth(s *TunnelSpec, transport string) {
	if !needsTLS(transport) {
		return
	}
	fmt.Println()
	tui.Info("If a reverse proxy (NGINX and the like) terminates TLS in front of this")
	tui.Info("tunnel, the default proof-of-session authorisation cannot match and the")
	tui.Info("tunnel is rejected. Simple auth sends the raw token instead, which works")
	tui.Info("through such a proxy.")
	tui.Warn("Only enable it when a trusted proxy is terminating the TLS — it hands the")
	tui.Warn("token to whatever does. Set the same answer on both ends.")
	s.SimpleAuth = tui.Confirm("Use simple token auth (for a TLS-terminating proxy in front)", s.SimpleAuth)
}
