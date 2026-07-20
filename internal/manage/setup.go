package manage

import (
	"fmt"
	"net"
	"strings"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/optimize"
	"github.com/backpack/backpack/internal/tui"
)

// transportEntry is one selectable transport. An empty value marks an entry
// that is listed for orientation but cannot be chosen yet.
type transportEntry struct {
	label, desc, value string
}

// transportGroups organises the transports into the three families they
// actually belong to, so the setup menu asks "which kind of connection?"
// before asking for a specific variant.
var transportGroups = []struct {
	label, desc string
	entries     []transportEntry
}{
	{"TCP", "reliable and simple — the safe default", []transportEntry{
		{"TCP", "plain & fast — start here if unsure", "tcp"},
		{"TCP Mux", "many streams over few connections — multiplexed", "tcpmux"},
		{"TCP + Stealth", "encrypted with no fingerprint — hardest to detect, for heavy filtering", "stealth"},
	}},
	{"UDP", "lower latency, better on lossy or throttled links", []transportEntry{
		{"UDP", "raw datagrams — for UDP-based services", "udp"},
		{"UDP + KCP", "reliable UDP with error correction — best on lossy links", "kcp"},
	}},
	{"WebSocket", "looks like normal web traffic — CDN friendly", []transportEntry{
		{"WS", "WebSocket — HTTP camouflage, CDN friendly", "ws"},
		{"WS Mux", "WebSocket — multiplexed", "wsmux"},
		{"WSS", "secure WebSocket — TLS encrypted", "wss"},
		{"WSS Mux", "TLS WebSocket — multiplexed", "wssmux"},
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
func choosePreset() string {
	opts := make([]tui.Option, len(presetOptions))
	for i, o := range presetOptions {
		opts[i] = tui.Option{Title: o.label, Desc: o.desc}
	}
	idx := tui.ChooseOpt("Performance preset:", opts)
	if idx < 0 {
		return PresetTurbo
	}
	return presetOptions[idx].value
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
		if s.Transport == "tcp" {
			s.AcceptUDP = tui.Confirm("Accept UDP traffic over the TCP transport (accept_udp)", s.AcceptUDP)
		}
	} else {
		s.ConnectionPool = tui.PromptInt("Connection pool size", s.ConnectionPool)
		s.AggressivePool = tui.Confirm("Enable aggressive pool", s.AggressivePool)
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

	s := TunnelSpec{Role: "server", Transport: transport}

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
	showForwardTargets(s.Ports)

	if needsTLS(transport) && !setupServerTLS(&s) {
		return
	}

	askProxyProtocol(&s)

	ApplyPreset(&s, choosePreset())
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

	// Backup addresses make the tunnel survive a filtered server IP: the client
	// tries each one in turn until something answers.
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
		tui.Info("Load balancing spreads the tunnel's connections over ALL of those")
		tui.Info("addresses at once instead of keeping them as spares.")
		tui.Warn("Only turn this on if every address reaches the SAME server — a")
		tui.Warn("second IP of it, another port, or a CDN edge in front of it.")
		s.LoadBalance = tui.Confirm("Enable load balancing", false)
	}

	ApplyPreset(&s, choosePreset())
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
func showForwardTargets(ports []string) {
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
			dest = strings.TrimSpace(dest)
			// A destination given as just a port means loopback there too.
			if !strings.Contains(dest, ":") {
				dest = "127.0.0.1:" + dest
			}
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
