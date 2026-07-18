package manage

import (
	"fmt"
	"net"
	"strings"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/optimize"
	"github.com/backpack/backpack/internal/tui"
)

// transportOptions is the ordered list shown to the user; index maps to value.
// This is the full transport set supported by the engine.
var transportOptions = []struct {
	label, desc, value string
}{
	{"TCP", "plain & fast — the safe default", "tcp"},
	{"TCP Mux", "many streams over few connections — multiplexed", "tcpmux"},
	{"UDP", "for UDP-based services", "udp"},
	{"WS", "WebSocket — HTTP camouflage, CDN friendly", "ws"},
	{"WSS", "secure WebSocket — TLS encrypted", "wss"},
	{"WS Mux", "WebSocket — multiplexed", "wsmux"},
	{"WSS Mux", "TLS WebSocket — multiplexed", "wssmux"},
}

func chooseTransport() string {
	opts := make([]tui.Option, len(transportOptions))
	for i, o := range transportOptions {
		opts[i] = tui.Option{Title: o.label, Desc: o.desc}
	}
	idx := tui.ChooseOpt("Select transport:", opts)
	if idx < 0 {
		return ""
	}
	return transportOptions[idx].value
}

// bestPerfPreset fills a spec with tuned values so the user only has to
// provide the essentials. These are the defaults behind the "Best Performance"
// label in the menu.
func bestPerfPreset(s *TunnelSpec) {
	s.Nodelay = true // disable Nagle → lowest latency
	s.KeepAlive = 75
	s.Heartbeat = 40
	s.ChannelSize = 4096
	s.ConnectionPool = 8 // enough warm connections without constant churn
	// AggressivePool is intentionally OFF: it keeps the pool topped up in a tight
	// loop and noticeably raises idle CPU. A normal pool is plenty.
	s.AggressivePool = false
	s.LogLevel = "info"
	// Large per-socket buffers keep the pipe full on high-latency Iran↔abroad
	// links (bandwidth-delay product), boosting throughput. The kernel ceilings
	// are raised to match by the Optimize step.
	s.SoRcvBuf = 8 * 1024 * 1024
	s.SoSndBuf = 8 * 1024 * 1024
	// SMUX tuning (used only if a mux transport is ever selected)
	s.MuxCon = 8
	s.MuxVersion = 2
	s.MuxFrameSize = 32768
	s.MuxRecvBuffer = 4194304
	s.MuxStreamBuffer = 65536
}

// applyManualTuning asks the advanced questions when the user opts out of the
// best-performance preset.
func applyManualTuning(s *TunnelSpec) {
	s.Nodelay = tui.Confirm("Enable TCP_NODELAY (lower latency)", true)
	s.KeepAlive = tui.PromptInt("Keepalive period (seconds)", 75)
	s.Heartbeat = tui.PromptInt("Heartbeat interval (seconds, 0 to disable)", 40)
	s.LogLevel = tui.PromptDefault("Log level (info/debug/warn/error)", "info")
	if s.Role == "server" {
		s.ChannelSize = tui.PromptInt("Channel size", 2048)
		if s.Transport == "tcp" {
			s.AcceptUDP = tui.Confirm("Accept UDP traffic over the TCP transport (accept_udp)", false)
		}
	} else {
		s.ConnectionPool = tui.PromptInt("Connection pool size", 8)
		s.AggressivePool = tui.Confirm("Enable aggressive pool", false)
	}
	if isMux(s.Transport) {
		s.MuxCon = tui.PromptInt("Mux connections/sessions", 8)
		s.MuxVersion = tui.PromptInt("Mux version (1 or 2)", 2)
		s.MuxFrameSize = tui.PromptInt("Mux frame size", 32768)
		s.MuxRecvBuffer = tui.PromptInt("Mux receive buffer", 4194304)
		s.MuxStreamBuffer = tui.PromptInt("Mux stream buffer", 65536)
	}
}

// setupServerTLS collects the certificate for wss/wssmux servers: either an
// auto-generated self-signed pair (fine — clients skip verification) or paths
// to an existing certificate. Returns false if setup should be aborted.
func setupServerTLS(s *TunnelSpec) bool {
	tui.Info("WSS transports need a TLS certificate. A self-signed one works out of")
	tui.Info("the box (clients don't verify it); use your own for a real domain.")
	choice := tui.ChooseOpt("TLS certificate:", []tui.Option{
		{Title: "Generate self-signed automatically", Desc: "recommended — works out of the box"},
		{Title: "Use existing certificate/key files", Desc: "e.g. Let's Encrypt paths"},
	})
	switch choice {
	case 0:
		host := strings.TrimSpace(tui.PromptDefault("Domain or IP to embed in the cert (optional)", ""))
		cert, key, err := EnsureSelfSignedCert(s.Name, host)
		if err != nil {
			tui.Error("Certificate generation failed: " + err.Error())
			tui.PressEnter()
			return false
		}
		s.TLSCert, s.TLSKey = cert, key
		tui.Success("Self-signed certificate created: " + cert)
	case 1:
		s.TLSCert = strings.TrimSpace(tui.Prompt("Path to TLS certificate (e.g. /etc/letsencrypt/live/x/fullchain.pem): "))
		s.TLSKey = strings.TrimSpace(tui.Prompt("Path to TLS key (e.g. /etc/letsencrypt/live/x/privkey.pem): "))
		if err := validCertPair(s.TLSCert, s.TLSKey); err != nil {
			tui.Error("Invalid certificate: " + err.Error())
			tui.PressEnter()
			return false
		}
	default:
		return false
	}
	return true
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
	ipv6 := tui.Confirm("Listen on IPv6 as well", false)
	bind := "0.0.0.0"
	if ipv6 {
		bind = "[::]"
	}
	s.BindAddr = bind + ":" + port

	defaultName := "server-" + port
	s.Name = uniqueName(tui.PromptDefault("Tunnel name", defaultName))

	suggested := randomToken(64)
	tui.Info("Suggested 64-char token (press Enter to accept — copy it to the client):")
	fmt.Println("  " + tui.Color(tui.Bold+tui.White, suggested))
	s.Token = tui.PromptDefault("Security token", suggested)

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

	if needsTLS(transport) && !setupServerTLS(&s) {
		return
	}

	best := tui.Confirm("Use Best Performance preset (recommended)", true)
	if best {
		bestPerfPreset(&s)
	} else {
		applyManualTuning(&s)
	}

	finishSetup(s, best)
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
	if strings.Contains(remoteHost, ":") && !strings.HasPrefix(remoteHost, "[") {
		remoteHost = "[" + remoteHost + "]" // IPv6 literal
	}
	s.RemoteAddr = remoteHost + ":" + remotePort

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

	best := tui.Confirm("Use Best Performance preset (recommended)", true)
	if best {
		bestPerfPreset(&s)
	} else {
		applyManualTuning(&s)
	}

	finishSetup(s, best)
}

// finishSetup persists the tunnel, optionally applies system-level tuning, and
// reports the result.
func finishSetup(s TunnelSpec, best bool) {
	if best {
		tui.Info("Applying system network optimizations for best performance...")
		optimize.ApplyQuiet()
	}

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
