package manage

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/tunnel/naive"
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
// There was an Experimental family here, holding xDi and — before it — IP
// spoofing. Both are direct-tunnel carriers now, for the same reason: a reverse
// tunnel is a control channel plus a pool of connections, each its own session,
// and neither carrier gives a receiver anything to tell those sessions apart
// by. They were offered here long after they had stopped being able to carry
// traffic in this shape. They are chosen under Direct, where the single session
// is what they can actually serve.
type transportGroup struct {
	label, desc string
	entries     []transportEntry
}

var transportGroups = []transportGroup{
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
}

// The terminal collects helper credentials; the web form cannot collect them.
// Keep these selections out of the ordinary transport API until it can.
func cliTransportGroups() []transportGroup {
	return append(append([]transportGroup(nil), transportGroups...), transportGroup{
		"HTTPS", "encrypted reverse TCP — Linux, matching versions on both sides", []transportEntry{
			{"Naive / HTTP2", "HTTPS proxy — certificate or a private trust file", "naive"},
			{"XHTTP / TLS", "HTTP/2 streaming — certificate or a private trust file", "xhttp"},
			{"REALITY / Vision", "TLS cover endpoint — no local certificate required", "reality"},
		},
	})
}

func managedTransport(value string) bool {
	return value == "naive" || value == "xhttp" || value == "reality"
}

func selectedTransport(s TunnelSpec) string {
	if s.NaiveServer.Enabled() || s.NaiveClient.Enabled() {
		return "naive"
	}
	if s.XrayServer.Enabled() {
		return s.XrayServer.Mode
	}
	if s.XrayClient.Enabled() {
		return s.XrayClient.Mode
	}
	return s.Transport
}

func managedEndpoint(s TunnelSpec) string {
	if s.Role == "server" {
		if s.NaiveServer.Enabled() {
			return s.NaiveServer.Listen
		}
		if s.XrayServer.Enabled() {
			return s.XrayServer.Listen
		}
		return s.BindAddr
	}
	if s.NaiveClient.Enabled() {
		return s.NaiveClient.Server
	}
	if s.XrayClient.Enabled() {
		return s.XrayClient.Server
	}
	return s.RemoteAddr
}

func clearManagedTransport(s *TunnelSpec) {
	s.NaiveServer, s.NaiveClient = config.NaiveServerConfig{}, config.NaiveClientConfig{}
	s.XrayServer, s.XrayClient = config.XrayServerConfig{}, config.XrayClientConfig{}
}

func managedHelperPath(tool string) string {
	version := map[string]string{"naive": "v154.0.8037.49-4", "sing-box": "v1.14.2", "xray": "v26.3.27"}[tool]
	return filepath.Join("/usr/local/lib/backpack/helpers", tool, version+"-"+runtime.GOARCH, tool)
}

func defaultString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func managedSecret(label, current, suggested string) string {
	if current != "" {
		value := tui.Prompt(label + " (Empty = Keep Current): ")
		if value == "" {
			return current
		}
		return value
	}
	if suggested != "" {
		return tui.PromptDefault(label, suggested)
	}
	return tui.Prompt(label + ": ")
}

func managedCertificate(s TunnelSpec, host string, cert, key string) (string, string, bool) {
	choice := tui.ChooseOpt("HTTPS Certificate", []tui.Option{
		{Title: "Existing Certificate And Key", Desc: "use PEM files, including an existing Let's Encrypt certificate"},
		{Title: "Generate Private Certificate", Desc: "copy its public PEM to Kharej and set the CA file there"},
	})
	switch choice {
	case 0:
		cert = tui.PromptDefault("Certificate File (fullchain.pem)", cert)
		key = tui.PromptDefault("Private Key File (privkey.pem)", key)
	case 1:
		host = tui.PromptDefault("Certificate Hostname Or IP (What Kharej Dials)", host)
		if host == "" {
			tui.Error("A certificate hostname or IP is required.")
			return "", "", false
		}
		var err error
		cert, key, err = EnsureSelfSignedCert(s.Name+"-https", host)
		if err != nil {
			tui.Error(err.Error())
			return "", "", false
		}
		tui.Info("Copy Only The Public Certificate To Kharej: " + cert)
	default:
		return "", "", false
	}
	return cert, key, true
}

func managedUUID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6], id[8] = id[6]&15|64, id[8]&63|128
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), nil
}

func managedRealityKey(current string) (string, string, error) {
	var key *ecdh.PrivateKey
	var err error
	if current == "" {
		key, err = ecdh.X25519().GenerateKey(rand.Reader)
	} else {
		var raw []byte
		raw, err = base64.RawURLEncoding.DecodeString(current)
		if err == nil {
			key, err = ecdh.X25519().NewPrivateKey(raw)
		}
	}
	if err != nil {
		return "", "", fmt.Errorf("invalid REALITY private key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(key.Bytes()), base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

func validateManagedSpec(s TunnelSpec) error {
	var c config.Config
	if _, err := toml.Decode(s.Render(), &c); err != nil {
		return err
	}
	if err := naive.Validate(&c); err != nil {
		return err
	}
	return naive.ValidateXray(&c)
}

// setupManagedCarrier collects a complete role-specific configuration before
// modifying the caller. The public endpoint and private reverse port are distinct.
func setupManagedCarrier(s *TunnelSpec, chosen, public, host string) bool {
	if !managedTransport(chosen) || (s.Role != "server" && s.Role != "client") {
		tui.Error("Invalid managed HTTPS transport or tunnel role.")
		return false
	}
	if runtime.GOOS != "linux" {
		tui.Error("Managed HTTPS transports require Linux.")
		return false
	}
	n := *s
	wasManaged := managedTransport(selectedTransport(n))
	internal := "3080"
	if wasManaged {
		if n.Role == "server" {
			internal = addrPort(n.BindAddr)
		} else {
			internal = addrPort(n.RemoteAddr)
		}
	}
	tui.Info("TCP Forwarding Only. Set The Same Internal Port And Security Token On Both Sides.")
	internal = strings.TrimSpace(tui.PromptDefault("Internal Reverse Port On Iran (Must Match Kharej)", internal))
	if !validPort(internal) || internal == addrPort(public) {
		tui.Error("The internal port must be valid and differ from the public HTTPS port.")
		return false
	}
	n.Transport = "tcp"
	n.AcceptUDP, n.SimpleAuth, n.LoadBalance, n.HealthFailover = false, false, false, false
	n.FallbackAddrs, n.FallbackTransports = nil, nil
	n.FallbackDwell, n.SOMark = 0, 0
	n.Proxy, n.LocalAddr, n.Interface, n.EdgeIP = "", "", "", ""
	n.ACMEDomain, n.ACMEEmail = "", ""
	if n.Role == "server" {
		n.BindAddr = net.JoinHostPort("127.0.0.1", internal)
	} else {
		n.RemoteAddr = net.JoinHostPort("127.0.0.1", internal)
	}
	clearManagedTransport(&n)
	if chosen == "naive" {
		if n.Role == "server" {
			x := s.NaiveServer
			x.Binary = tui.PromptDefault("Sing-Box Binary", defaultString(x.Binary, managedHelperPath("sing-box")))
			x.Listen = public
			x.Username = tui.PromptDefault("Naive Username (Same On Kharej)", defaultString(x.Username, "backpack"))
			x.Password = managedSecret("Naive Password (Same On Kharej)", x.Password, randomToken(32))
			var ok bool
			x.Certificate, x.Key, ok = managedCertificate(n, host, x.Certificate, x.Key)
			if !ok {
				return false
			}
			n.NaiveServer = x
		} else {
			x := s.NaiveClient
			x.Binary = tui.PromptDefault("NaiveProxy Binary", defaultString(x.Binary, managedHelperPath("naive")))
			x.Server = public
			x.Username = tui.PromptDefault("Naive Username (From Iran)", defaultString(x.Username, "backpack"))
			x.Password = managedSecret("Naive Password (From Iran)", x.Password, "")
			x.CAFile = tui.PromptDefault("CA File (Required For A Private Certificate; Empty For Public TLS)", x.CAFile)
			n.NaiveClient = x
		}
	} else {
		id, serverName, path, shortID := "", host, "", ""
		if n.Role == "server" {
			id, serverName, path, shortID = s.XrayServer.UUID, defaultString(s.XrayServer.ServerName, host), s.XrayServer.Path, s.XrayServer.ShortID
		} else {
			id, serverName, path, shortID = s.XrayClient.UUID, defaultString(s.XrayClient.ServerName, host), s.XrayClient.Path, s.XrayClient.ShortID
		}
		if id == "" && n.Role == "server" {
			var err error
			id, err = managedUUID()
			if err != nil {
				tui.Error(err.Error())
				return false
			}
		}
		id = tui.PromptDefault("VLESS UUID (Same On Both Sides)", id)
		serverName = tui.PromptDefault("TLS Server Name (REALITY: Cover Hostname)", serverName)
		if chosen == "xhttp" {
			if path == "" && n.Role == "server" {
				path = "/" + randomToken(24)
			}
			path = tui.PromptDefault("XHTTP Path (Same On Both Sides)", path)
		} else {
			if shortID == "" && n.Role == "server" {
				var raw [8]byte
				if _, err := rand.Read(raw[:]); err != nil {
					tui.Error(err.Error())
					return false
				}
				shortID = hex.EncodeToString(raw[:])
			}
			shortID = tui.PromptDefault("REALITY Short ID (Same On Both Sides)", shortID)
		}
		if n.Role == "server" {
			x := config.XrayServerConfig{Listen: public, Mode: chosen, UUID: id, ServerName: serverName, Path: path}
			x.Binary = tui.PromptDefault("Xray Binary", defaultString(s.XrayServer.Binary, managedHelperPath("xray")))
			if chosen == "xhttp" {
				x.Host = tui.PromptDefault("HTTP Host (Optional; Same On Kharej)", s.XrayServer.Host)
				var ok bool
				x.Certificate, x.Key, ok = managedCertificate(n, serverName, s.XrayServer.Certificate, s.XrayServer.Key)
				if !ok {
					return false
				}
			} else {
				x.ShortID = shortID
				x.Target = tui.PromptDefault("REALITY Cover Endpoint (Reachable TLS 1.3/H2 Host:Port)", s.XrayServer.Target)
				key := managedSecret("REALITY Private Key (Empty = Generate On First Setup)", s.XrayServer.PrivateKey, "")
				var pub string
				var err error
				x.PrivateKey, pub, err = managedRealityKey(key)
				if err != nil {
					tui.Error(err.Error())
					return false
				}
				tui.Info("REALITY Public Key For Kharej: " + pub)
			}
			n.XrayServer = x
		} else {
			x := config.XrayClientConfig{Server: public, Mode: chosen, UUID: id, ServerName: serverName, Path: path}
			x.Binary = tui.PromptDefault("Xray Binary", defaultString(s.XrayClient.Binary, managedHelperPath("xray")))
			if chosen == "xhttp" {
				x.Host = tui.PromptDefault("HTTP Host (Optional; From Iran)", s.XrayClient.Host)
				x.CAFile = tui.PromptDefault("CA File (Required For A Private Certificate; Empty For Public TLS)", s.XrayClient.CAFile)
			} else {
				x.ShortID = shortID
				x.PublicKey = tui.PromptDefault("REALITY Public Key (From Iran)", s.XrayClient.PublicKey)
			}
			n.XrayClient = x
		}
	}
	tui.StopIfInputGone()
	if err := validateManagedSpec(n); err != nil {
		tui.Error("HTTPS configuration: " + err.Error())
		return false
	}
	if n.Role == "server" && !fileExists(app.ConfigPath(n.Name)) {
		for _, addr := range []string{public, n.BindAddr} {
			if busy := reverseBusyPorts(n.Ports, addr, "tcp"); len(busy) > 0 {
				tui.Error("HTTPS ports conflict with forwarded ports: " + strings.Join(busy, ", "))
				return false
			}
			if portHeld(addr) {
				tui.Error("Listener already in use: " + addr)
				return false
			}
		}
	}
	*s = n
	return true
}

// chooseTransport walks the family menu and then the variant menu. It returns
// an empty string when the user backs out at either level.
func chooseTransport() string {
	groups := cliTransportGroups()
	for {
		groupOpts := make([]tui.Option, len(groups))
		for i, g := range groups {
			groupOpts[i] = tui.Option{Title: g.label, Desc: g.desc}
		}
		gi := tui.ChooseOpt("Select Transport Family", groupOpts)
		if gi < 0 {
			return ""
		}
		group := groups[gi]

		entryOpts := make([]tui.Option, len(group.entries))
		for i, e := range group.entries {
			entryOpts[i] = tui.Option{Title: e.label, Desc: e.desc}
		}
		ei := tui.ChooseOpt("Select "+group.label+" Transport", entryOpts)
		if ei < 0 {
			// Back to the family list rather than out of setup entirely.
			continue
		}
		if group.entries[ei].value == "" {
			tui.Warn(group.entries[ei].label + " Is Not Available Yet.")
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
		opts[i] = tui.Option{Title: o.Label, Desc: o.Desc}
	}
	idx := tui.ChooseOpt("How Should The Tunnel Be Tuned?", opts)
	if idx < 0 {
		return PresetTurbo
	}
	return options[idx].Value
}

// applyManualTuning asks the advanced questions for users who want to override
// the preset. It runs after ApplyPreset, so every prompt starts from the
// preset's value and anything left untouched keeps that value.
func applyManualTuning(s *TunnelSpec) {
	s.Nodelay = tui.Confirm("TCP_NODELAY (Lower Latency)", s.Nodelay)
	s.KeepAlive = tui.PromptInt("Keepalive Period (Seconds)", s.KeepAlive)
	s.Heartbeat = tui.PromptInt("Heartbeat Interval (Seconds, 0 = Off)", s.Heartbeat)
	s.LogLevel = tui.PromptDefault("Log Level (info/debug/warn/error)", s.LogLevel)
	// JSON is for feeding a log collector or a script; a person reading
	// journalctl is better served by the default text format.
	if tui.Confirm("Write Logs As JSON (For Log Collectors)", s.LogFormat == "json") {
		s.LogFormat = "json"
	} else {
		s.LogFormat = ""
	}
	if s.Role == "server" {
		s.ChannelSize = tui.PromptInt("Channel Size", s.ChannelSize)
		// UDP forwarding is not asked here: it is asked in the main setup flow,
		// next to the forwarded ports it describes. Burying it under the
		// advanced settings — which default to "no" — meant a fresh install
		// never saw the question at all, and an Xray or WireGuard inbound came
		// up with nothing explaining why only half of it worked.
	} else {
		s.ConnectionPool = tui.PromptInt("Connection Pool Size", s.ConnectionPool)
		s.AggressivePool = tui.Confirm("Aggressive Pool", s.AggressivePool)
	}
	// The MSS clamp is deliberately not part of any preset: it describes the
	// path the tunnel crosses, not how hard the tunnel is being pushed. Keep it
	// at 0 unless Diagnose reports the path cannot carry full-sized packets —
	// it prints the value, and both ends need the same one.
	if !isDatagram(s.Transport) {
		s.MSS = tui.PromptInt("TCP MSS Clamp (Bytes, 0 = Automatic; Both Ends The Same)", s.MSS)
	}
	if isMux(s.Transport) {
		s.MuxCon = tui.PromptInt("Mux Connections", s.MuxCon)
		s.MuxVersion = tui.PromptInt("Mux Version (1 Or 2)", s.MuxVersion)
		s.MuxFrameSize = tui.PromptInt("Mux Frame Size", s.MuxFrameSize)
		s.MuxRecvBuffer = tui.PromptInt("Mux Receive Buffer", s.MuxRecvBuffer)
		s.MuxStreamBuffer = tui.PromptInt("Mux Stream Buffer", s.MuxStreamBuffer)
	}
	if isKCP(s.Transport) {
		s.KCPMTU = tui.PromptInt("KCP MTU (Bytes, Below The Path MTU)", s.KCPMTU)
		s.KCPInterval = tui.PromptInt("KCP Interval (ms)", s.KCPInterval)
		s.KCPSndWnd = tui.PromptInt("KCP Send Window (Packets)", s.KCPSndWnd)
		s.KCPRcvWnd = tui.PromptInt("KCP Receive Window (Packets)", s.KCPRcvWnd)
		s.KCPDataShards = tui.PromptInt("FEC Data Shards (0 = Off)", s.KCPDataShards)
		s.KCPParityShards = tui.PromptInt("FEC Parity Shards", s.KCPParityShards)
	}
	// Zero-copy forwarding, offered only where it can actually engage: the
	// kernel path needs two plain TCP sockets, so a mux, websocket or datagram
	// transport would take the setting and quietly ignore it. It is the
	// fastest path and the least proven one; nothing about it reaches the
	// wire, so the two ends need not agree.
	if s.Transport == "tcp" && !managedTransport(selectedTransport(*s)) {
		s.ZeroCopy = tui.Confirm("Zero-Copy Forwarding (Experimental)", s.ZeroCopy)
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
	// Self-signed encrypts exactly as well — the client is Backpack's own code
	// and does not verify it. A real certificate matters for how the
	// connection looks from outside, and it is what a CDN requires.
	choice := tui.ChooseOpt("TLS Certificate", []tui.Option{
		{Title: "Self-Signed", Desc: "generated now — works anywhere, including on a bare IP"},
		{Title: "Let's Encrypt", Desc: "free and real — needs a domain pointing at this server"},
		{Title: "Existing Files", Desc: "a certificate and key you already have on disk"},
	})

	switch choice {
	case 0:
		host := strings.TrimSpace(tui.PromptDefault("Domain Or IP In The Certificate (Optional)", ""))
		return generateSelfSigned(s, host)

	case 1:
		// Off 443, validation happens over port 80, which must then be free
		// and open; on 443 it goes over the tunnel's own listener.
		if p := addrPort(s.BindAddr); p != "443" {
			tui.Warn("Port 80 Must Be Free And Open For Let's Encrypt.")
		}

		domain, email, ok := promptACMEDomain("", "")
		if !ok {
			return false
		}
		s.ACMEDomain, s.ACMEEmail = domain, email
		// The self-signed pair is generated anyway. It is what the config still
		// points at, and it is the fallback if issuance fails — without it a
		// failed ACME request would leave the tunnel with no certificate at all.
		return generateSelfSigned(s, domain)

	case 2:
		s.TLSCert = strings.TrimSpace(tui.Prompt("Certificate File (fullchain.pem): "))
		s.TLSKey = strings.TrimSpace(tui.Prompt("Key File (privkey.pem): "))
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
	// Needs: the domain's A record on this server, port 80 reachable (or the
	// tunnel on 443), and a way out to acme-v02.api.letsencrypt.org.
	domain = strings.TrimSpace(tui.PromptDefault("Domain (A Record Pointing Here)", currentDomain))
	if domain == "" {
		tui.Error("A domain is required.")
		tui.PressEnter()
		return "", "", false
	}
	if net.ParseIP(domain) != nil {
		tui.Error("Let's Encrypt needs a domain, not an IP.")
		tui.PressEnter()
		return "", "", false
	}

	if ips, err := net.LookupHost(domain); err != nil {
		tui.Error("That domain does not resolve: " + err.Error())
		if !tui.Confirm("Use It Anyway", false) {
			return "", "", false
		}
	} else {
		tui.Info("Resolves To: " + strings.Join(ips, ", "))
		if mine := PublicIPv4(); mine != "" && mine != "-" && !contains(ips, mine) {
			tui.Error("It does not point at this server (" + mine + "); issuance would fail.")
			if !tui.Confirm("Use It Anyway", false) {
				return "", "", false
			}
		}
	}

	email = strings.TrimSpace(tui.PromptDefault("Email For Expiry Warnings (Optional)", currentEmail))
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
	// The interface, local address and next hop are read from this machine's
	// routing table. What is left is what the flags on the wire look like —
	// varied only if the path is known to match on the pattern; each end
	// decides its own — and an override for a host where the lookup is wrong.
	opts := network.SuggestedTCPFlagCycles()
	menu := make([]tui.Option, len(opts))
	for i, o := range opts {
		menu[i] = tui.Option{Title: o.Value, Desc: o.Desc}
	}
	if i := tui.ChooseOpt("TCP Flag Pattern", menu); i > 0 {
		s.PckFlags = strings.Split(opts[i].Value, ",")
	} else {
		s.PckFlags = nil // the default, left out of the config entirely
	}

	if tui.Confirm("Override The Automatic Interface / Gateway Detection", false) {
		if names := routableInterfaces(); len(names) > 0 {
			tui.Info("Interfaces: " + strings.Join(names, ", "))
		}
		for {
			raw := strings.TrimSpace(tui.PromptDefault("Interface (Blank = Automatic)", ""))
			if raw == "" {
				break
			}
			if _, err := net.InterfaceByName(raw); err != nil {
				tui.Error(fmt.Sprintf("No such interface: %v", err))
				continue
			}
			s.PckInterface = raw
			break
		}
		for {
			raw := strings.TrimSpace(tui.PromptDefault("Gateway MAC (Blank = Automatic)", ""))
			if raw == "" {
				break
			}
			if _, err := net.ParseMAC(raw); err != nil {
				tui.Error(fmt.Sprintf("Not a MAC address: %v", err))
				continue
			}
			s.PckGatewayMAC = raw
			break
		}
	}
}

// askSpoofCarrier asks the forged-source carrier's questions on one side: what
// both ends must match (askSpoofShared), then what is this side's own
// (askSpoofLocal). A kharej built from a setup link has the shared half from
// the link and is asked only askSpoofLocal.
func askSpoofCarrier(sc *config.SpoofConfig, onIran bool) {
	askSpoofShared(sc)
	askSpoofLocal(sc, onIran)
	spoofSummary(*sc, onIran)
}

// askSpoofShared is what the two ends must answer alike: how the packets look,
// and Stealth, which changes the wire.
//
// Each question starts from what sc already holds, so an edit keeps what it is
// not asked to change.
func askSpoofShared(sc *config.SpoofConfig) {
	fmt.Println()
	if sc.SpoofProfile == "" || tui.Confirm("Change Packet Profile (Now "+sc.SpoofProfile+")", false) {
		sc.SpoofProfile = askSpoofProfile("Packet Profile")
	}
	split := sc.SpoofUplink != "" || sc.SpoofDownlink != ""
	if tui.Confirm("Different Profile Each Direction", split) {
		if !split || tui.Confirm("Change The Two Profiles", false) {
			sc.SpoofUplink = askSpoofProfile("Uplink Profile (Kharej → Iran)")
			sc.SpoofDownlink = askSpoofProfile("Downlink Profile (Iran → Kharej)")
		}
	} else {
		sc.SpoofUplink, sc.SpoofDownlink = "", ""
	}
	if tui.Confirm("Stealth (Padding And Header Cosmetics)", spoofStealthOn(*sc)) {
		applySpoofStealth(sc)
	} else if spoofStealthOn(*sc) {
		clearSpoofStealth(sc)
	}
}

// askSpoofLocal is this side's own: the other server's real address, which a
// forged packet cannot tell it, the source it forges, and the interface.
func askSpoofLocal(sc *config.SpoofConfig, onIran bool) {
	other := "Iran"
	if onIran {
		other = "Kharej"
	}
	for net.ParseIP(sc.SpoofPeerIP).To4() == nil {
		sc.SpoofPeerIP = strings.TrimSpace(tui.Prompt(other + " Real IPv4: "))
		if net.ParseIP(sc.SpoofPeerIP).To4() == nil {
			tui.Error("Enter an IPv4 address, like 203.0.113.10.")
			tui.StopIfInputGone()
		}
	}
	for {
		raw := strings.TrimSpace(tui.PromptDefault("Forged Source IPv4 (Comma Separated, Blank = None)", strings.Join(spoofSources(*sc), ", ")))
		pool, err := parseIPv4List(raw, "forged source")
		if err != nil {
			tui.Error(err.Error())
			tui.StopIfInputGone()
			continue
		}
		sc.SpoofSrcIP, sc.SpoofSrcPool = "", nil
		if len(pool) > 0 {
			sc.SpoofSrcIP = pool[0]
		}
		if len(pool) > 1 {
			sc.SpoofSrcPool = pool
		}
		break
	}
	// Only asked where there is a choice.
	if names := routableInterfaces(); len(names) > 1 {
		for {
			iface := strings.TrimSpace(tui.PromptDefault("Interface ("+strings.Join(names, ", ")+", Blank = Auto)", sc.SpoofInterface))
			if iface == "" {
				sc.SpoofInterface = ""
				break
			}
			if _, err := net.InterfaceByName(iface); err != nil {
				tui.Error(fmt.Sprintf("No such interface: %v", err))
				continue
			}
			sc.SpoofInterface = iface
			break
		}
	}
}

// spoofSources is the forged source or pool a config holds.
func spoofSources(sc config.SpoofConfig) []string {
	if len(sc.SpoofSrcPool) > 0 {
		return sc.SpoofSrcPool
	}
	if sc.SpoofSrcIP != "" {
		return []string{sc.SpoofSrcIP}
	}
	return nil
}

// applySpoofStealth turns on the obfuscation the Stealth question stands for.
//
// It is a named set rather than a line in the wizard for one reason: two of
// these change what goes on the wire and so must match at the far end, and
// three do not. An operator setting them one at a time will eventually set a
// wire-changing one on a single end, and the result is a tunnel that connects
// and carries nothing — the failure this whole carrier is worst at explaining.
// Setting them together means "the same answer on both ends" is one answer.
//
// The values are the reference implementation's: a padding ceiling of 64 bytes,
// and the ephemeral range for the source port.
func applySpoofStealth(sc *config.SpoofConfig) {
	sc.SpoofPadding, sc.SpoofPaddingMax = true, 64
	sc.SpoofTTLJitter = true
	sc.SpoofRandomDSCP = true
	sc.SpoofShufflePort = true
	sc.SpoofPortMin, sc.SpoofPortMax = 49152, 65535
	// Only the tcp profile carries a TLS record header; on the others the flag
	// is read and ignored, so setting it would be a setting that does nothing.
	if up, down := network.ResolveSpoofDirections(sc.SpoofProfile, sc.SpoofUplink, sc.SpoofDownlink); up == "tcp" || down == "tcp" {
		sc.SpoofFakeTLS = true
	}
}

// spoofStealthOn reports whether the wire-changing half of Stealth is in force,
// which is what a summary or an edit screen has to say out loud: those are the
// settings the other end must match.
func spoofStealthOn(sc config.SpoofConfig) bool {
	return sc.SpoofPadding || sc.SpoofFakeTLS
}

// spoofSummary repeats the spoofing answers in three lines.
func spoofSummary(sc config.SpoofConfig, onIran bool) {
	other := "Iran"
	if onIran {
		other = "Kharej"
	}
	profile := sc.SpoofProfile
	if sc.SpoofUplink != "" || sc.SpoofDownlink != "" {
		profile = "uplink " + orDefault(sc.SpoofUplink, sc.SpoofProfile) + ", downlink " + orDefault(sc.SpoofDownlink, sc.SpoofProfile)
	}
	src := strings.Join(spoofSources(sc), ", ")
	if src == "" {
		src = "none (real address)"
	}
	stealth := "off"
	if spoofStealthOn(sc) {
		stealth = "on"
	}
	fmt.Println()
	tui.Info("Spoof: " + profile + " · Forged Source: " + src + " · Stealth: " + stealth)
	tui.Info(other + " Real IP: " + sc.SpoofPeerIP)
}

// askSpoofProfile prompts for one packet profile and returns its config value.
func askSpoofProfile(title string) string {
	// Built from the same list the panel offers, in the same order, so the two
	// screens cannot drift apart — and so a profile added to the carrier shows
	// up in both without either being edited.
	opts := make([]tui.Option, 0, len(SpoofProfiles()))
	for _, p := range SpoofProfiles() {
		opts = append(opts, tui.Option{Title: p.Label, Desc: p.Desc})
	}
	chosen := tui.ChooseOpt(title, opts)
	if chosen < 0 || chosen >= len(spoofProfiles) {
		return "udp" // the recommendation, and what going back should not change
	}
	return spoofProfiles[chosen]
}

// askProxyProtocol offers to forward the real client IP to the service behind
// the tunnel. Without it that service sees every connection as coming from the
// tunnel itself, which is why per-user device limits in VPN panels stop working
// once traffic is tunnelled.
func askProxyProtocol(s *TunnelSpec) {
	if !supportsProxyProtocol(s.Transport) {
		return
	}
	// Each connection then carries a PROXY v2 header with the real client IP.
	// A service not set to accept it (X-UI / Marzban: "Accept Proxy Protocol")
	// reads the header as data and every connection breaks — hence the label.
	s.ProxyProtocol = tui.Confirm("Send Real Client IP (PROXY Protocol — The Service Must Accept It)", false)
}

// uniqueName ensures the chosen name is valid and not already taken.
func uniqueName(name string) string {
	for {
		switch {
		case !validName(name):
			tui.Warn(fmt.Sprintf("Invalid Name %q — Letters, Digits, Dots, Dashes (Max 40).", name))
		case fileExists(app.ConfigPath(name)):
			tui.Warn(fmt.Sprintf("%q Already Exists.", name))
		default:
			return name
		}
		tui.StopIfInputGone()
		name = tui.Prompt("Another Name: ")
	}
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
	tui.Info("Must Be Listening On The Kharej:")
	for _, t := range targets {
		fmt.Printf("  %s%s%s  →  %s%s%s\n",
			tui.Gray, t.exposed, tui.Reset,
			tui.Bold+tui.White, t.dest, tui.Reset)
	}
	fmt.Println()
	tui.Warn("Check: ss -tlnp | grep <port>   (Bound To A Public IP? Map It: 443=<IP>:443)")
	// A firewall opened for TCP is not opened for UDP, which is the thing
	// people miss — so say which one this tunnel actually needs.
	if acceptUDP {
		tui.Warn("Firewall Here: ufw allow <port>/tcp && ufw allow <port>/udp")
	} else {
		tui.Warn("Firewall Here: ufw allow <port>/tcp   (TCP Only)")
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
		return tui.Confirm("Use It Anyway", false)
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
			tui.Warn("Has IPv4 And IPv6 — It May Connect Over IPv6. Use The IPv4 If Unsure.")
			return tui.Confirm("Continue", false)
		}
		return tui.Confirm("Continue", true)
	}

	// Proxied. Whether that can work depends entirely on the transport.
	tui.Error("That address belongs to " + cdn + ", not to a server.")
	if isWS(transport) && cdnPort(port) {
		tui.Warn("WebSocket Through " + cdn + " Can Work — Use Let's Encrypt On The Iran Side.")
	} else {
		tui.Warn("Will Not Work Through A CDN — Use DNS-Only, The IP, Or WSS On 443.")
	}
	return tui.Confirm("Continue Anyway", false)
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
	// Behind a reverse proxy that terminates TLS (NGINX and the like) the
	// default proof-of-session cannot match; the raw token works through it,
	// and hands the token to whatever terminates the TLS. Same answer on both
	// ends.
	s.SimpleAuth = tui.Confirm("Simple Token Auth (Only Behind A TLS-Terminating Proxy)", s.SimpleAuth)
}

// clearSpoofStealth is applySpoofStealth's opposite: it puts the carrier back to
// plain packets, including the bounds the knobs carried, so a config that has
// been switched off does not keep a port range and a padding ceiling that
// nothing reads.
func clearSpoofStealth(sc *config.SpoofConfig) {
	sc.SpoofPadding, sc.SpoofPaddingMax = false, 0
	sc.SpoofTTLJitter = false
	sc.SpoofRandomDSCP = false
	sc.SpoofShufflePort = false
	sc.SpoofPortMin, sc.SpoofPortMax = 0, 0
	sc.SpoofFakeTLS = false
}
