package manage

import (
	"fmt"
	"net"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/tui"
)

// Editing a direct tunnel after it exists.
//
// The two things an operator actually comes back to change are the forwarded
// ports and whether UDP rides along. Everything else — the token, the
// addresses, the transport — has to match the other machine, so changing it
// here alone would only break the tunnel; those are shown but not offered.
//
// Edits go through the parsed config and are written back through the same
// renderer the wizard uses, so an edited file looks exactly like a fresh one
// and keeps its comments.

// editDirectMenu is the per-tunnel editor for the two direct kinds.
func editDirectMenu(t Tunnel) {
	for {
		cfg, err := LoadTunnelConfig(t.Name)
		if err != nil {
			tui.Error("Cannot read the config: " + err.Error())
			tui.PressEnter()
			return
		}

		tui.Clear()
		tui.Title("Edit — " + t.Name)
		fmt.Println()

		if cfg.Direct.Enabled() {
			if !editDirectPorts(t, cfg) {
				return
			}
			continue
		}
		if !editL3Ports(t, cfg) {
			return
		}
	}
}

// editDirectPorts shows a [direct] tunnel and offers what is safe to change.
// It returns false when the operator is done.
func editDirectPorts(t Tunnel, cfg config.Config) bool {
	d := cfg.Direct
	iran := d.ResolvedRole() == "edge"

	tui.Info("Kind         : Direct, Forwarded Ports")
	tui.Info("This Server  : " + directRole(d.ResolvedRole()))
	tui.Info("Transport    : " + orDefault(d.Transport, "tcp"))
	if iran {
		tui.Info("Dials        : " + d.Addr)
		tui.Info("Ports        : " + strings.Join(d.Ports, ", "))
		tui.Info("UDP          : " + onOff(d.AcceptUDP))
		tui.Info("Tuning       : " + presetLabel(d.Preset) + fmt.Sprintf(", %d session(s)", max(d.Sessions, 1)))
		tui.Info("Limits       : " + limitsLabel(d.MaxConnections, d.BandwidthMbps))
	} else {
		tui.Info("Listens On   : " + d.Addr)
		fmt.Println()
		tui.Warn("Ports Are Set On The Iran Server.")
	}
	fmt.Println()

	if !iran {
		tui.PressEnter()
		return false
	}

	switch tui.ChooseOpt("Edit", []tui.Option{
		{Title: "Forwarded Ports", Desc: ""},
		{Title: "UDP Forwarding", Desc: "now " + onOff(d.AcceptUDP)},
		{Title: "Limits", Desc: "now " + limitsLabel(d.MaxConnections, d.BandwidthMbps)},
		{Title: "Preset", Desc: "now " + presetLabel(d.Preset)},
		{Title: "TCP MSS Clamp", Desc: "now " + directMSSLabel(d.MSS)},
		{Title: "Show Token", Desc: ""},
		{Title: "Kharej Address", Desc: "now " + d.Addr},
	}) {
	case 0:
		raw := tui.Prompt("Forwarded Ports (e.g. 443,8080=80): ")
		ports := parsePorts(raw)
		if len(ports) == 0 {
			tui.Error("No valid ports.")
			tui.PressEnter()
			return true
		}
		if err := validatePortSpecs(ports); err != nil {
			tui.Error(err.Error())
			tui.PressEnter()
			return true
		}
		d.Ports = ports
		showForwardTargets(ports, d.AcceptUDP)
		saveDirect(t, d)
	case 1:
		d.AcceptUDP = tui.Confirm("Carry UDP As Well As TCP", d.AcceptUDP)
		saveDirect(t, d)
	case 2:
		d.MaxConnections = tui.PromptInt("Max Connections (0 = No Limit)", d.MaxConnections)
		d.BandwidthMbps = tui.PromptInt("Bandwidth Mbit/s (0 = No Limit)", d.BandwidthMbps)
		saveDirect(t, d)
	case 3:
		p := chooseDirectPreset()
		d.Preset = p.Name
		d.MaxFrameSize = p.MuxFrameSize
		d.MaxReceiveBuffer = p.MuxReceiveBuffer
		d.MaxStreamBuffer = p.MuxStreamBuffer
		if p.Sessions > d.Sessions {
			d.Sessions = p.Sessions
		}
		saveDirect(t, d)
	case 4:
		fmt.Println()
		tui.Warn("Same Value On Both Servers; 1360 Is A Safe First Try.")
		mss := tui.PromptInt("TCP MSS Clamp (0 = Auto)", d.MSS)
		if mss != 0 && (mss < minMSS || mss > maxMSS) {
			tui.Error(fmt.Sprintf("Between %d and %d, or 0.", minMSS, maxMSS))
			tui.PressEnter()
			return true
		}
		d.MSS = mss
		saveDirect(t, d)
	case 5:
		fmt.Println()
		tui.Info("Token (Copy Exactly):")
		fmt.Println("  " + tui.Color(tui.Bold+tui.White, d.Token))
		tui.PressEnter()
	case 6:
		addr, ok := askPeerAddress("Kharej", d.Addr)
		if !ok {
			return true
		}
		d.Addr = addr
		saveDirect(t, d)
	default:
		return false
	}
	return true
}

// directMSSLabel renders the setting for a menu line, saying what a zero
// actually means rather than printing it.
func directMSSLabel(mss int) string {
	if mss <= 0 {
		return "off (the kernel decides)"
	}
	return fmt.Sprintf("%d bytes", mss)
}

// editL3Ports does the same for an [l3] tunnel.
func editL3Ports(t Tunnel, cfg config.Config) bool {
	l := cfg.L3
	iran := !strings.EqualFold(strings.TrimSpace(l.Mode), "listen")

	tui.Info("Kind         : Direct (Layer 3)")
	tui.Info("This Server  : " + l3Role(l.Mode))
	tui.Info("Carrier      : " + orDefault(l.Carrier, "udp"))
	tui.Info("Wrapping     : " + l3EncapLabel(l))
	tui.Info("Address      : " + l.Addr)
	tui.Info("Interface    : " + orDefault(l.Iface, "bp0") + "  " + l.LocalIP + " ↔ " + l.PeerIP)
	tui.Info("MTU          : " + fmt.Sprint(l.MTU))
	tui.Info("Tuning       : " + presetLabel(l.Preset) + ", " + orDefault(l.Qdisc, "fq_codel"))
	if len(l.Ports) > 0 {
		tui.Info("Ports        : " + strings.Join(l.Ports, ", "))
		tui.Info("UDP          : " + onOff(l.AcceptUDP))
	}
	fmt.Println()

	options := []tui.Option{
		{Title: "MTU", Desc: "now " + fmt.Sprint(l.MTU)},
		{Title: "TCP MSS Clamp", Desc: "now " + mssClampLabel(l.MSSClamp, l.MTU)},
		{Title: "Show Token", Desc: ""},
	}
	// The carrier's own screen, for the one carrier that has settings worth
	// changing after the fact. It goes last on both sides, which is what keeps
	// l3EditAction's index shift correct without touching it: the entry sits at
	// the end of the list whether or not the two Iran-only entries are above it.
	if strings.EqualFold(strings.TrimSpace(l.Carrier), "spoof") {
		options = append(options, tui.Option{
			Title: "IP Spoofing",
			Desc:  "now " + spoofCarrierSummary(l.SpoofConfig),
		})
	}
	if iran {
		options = append([]tui.Option{
			{Title: "Forwarded Ports", Desc: ""},
			{Title: "UDP Forwarding", Desc: "now " + onOff(l.AcceptUDP)},
		}, options...)
	}
	// Where the other machine is, when this side has to be told: the Iran side
	// dials the kharej, and a spoof kharej cannot learn the Iran server's real
	// address from packets whose source is forged. Last in the list, after
	// l3EditAction's range, and handled before it.
	peer := -1
	switch {
	case iran:
		peer = len(options)
		options = append(options, tui.Option{Title: "Kharej Address", Desc: "now " + l.Addr})
	case strings.EqualFold(strings.TrimSpace(l.Carrier), "spoof"):
		peer = len(options)
		options = append(options, tui.Option{Title: "Iran Real IP",
			Desc: "now " + orDefault(l.SpoofPeerIP, "not set")})
	}

	chosen := tui.ChooseOpt("Edit", options)
	if chosen >= 0 && chosen == peer {
		if iran {
			addr, ok := askPeerAddress("Kharej", l.Addr)
			if !ok {
				return true
			}
			movePeer(&l, addr)
		} else {
			ip := strings.TrimSpace(tui.PromptDefault("Iran Real IPv4", l.SpoofPeerIP))
			if net.ParseIP(ip) == nil || net.ParseIP(ip).To4() == nil {
				tui.Error("Not an IPv4 address.")
				tui.PressEnter()
				return true
			}
			l.SpoofPeerIP = ip
		}
		saveL3(t, l)
		return true
	}
	choice, ok := l3EditAction(chosen, iran)
	if !ok {
		return false
	}

	switch choice {
	case 0:
		raw := tui.Prompt("Forwarded Ports (Blank = None): ")
		if strings.TrimSpace(raw) == "" {
			l.Ports = nil
		} else {
			ports := parsePorts(raw)
			if err := validatePortSpecs(ports); err != nil {
				tui.Error(err.Error())
				tui.PressEnter()
				return true
			}
			l.Ports = ports
		}
		saveL3(t, l)
	case 1:
		l.AcceptUDP = tui.Confirm("Carry UDP As Well As TCP", l.AcceptUDP)
		saveL3(t, l)
	case 2:
		fmt.Println()
		l.MTU = tui.PromptInt("Tunnel MTU (Lower It If Downloads Stall)", l.MTU)
		saveL3(t, l)
	case 3:
		fmt.Println()
		l.MSSClamp = tui.PromptInt("TCP MSS Clamp (0 = From MTU, -1 = Off)", l.MSSClamp)
		saveL3(t, l)
	case 4:
		fmt.Println()
		tui.Info("Token (Copy Exactly):")
		fmt.Println("  " + tui.Color(tui.Bold+tui.White, l.Token))
		tui.PressEnter()
	case 5:
		if editL3Spoof(t, l) {
			return true
		}
	default:
		return false
	}
	return true
}

// l3EditActionShift is how far the kharej side's indices are below the actions
// they stand for: it is not offered the two Iran-only entries at the top.
const l3EditActionShift = 2

// l3EditAction turns the entry the operator picked into the action to take,
// and reports false when they asked to go back instead.
//
// Shifting the indices is easy; shifting the "go back" answer with them is the
// mistake — ChooseOpt answers -1 for that, and -1 shifted up is a real action.
// Leaving the editor would have changed a setting and restarted the tunnel.
//
// Separate from the editor so the arithmetic can be tested without a terminal.
func l3EditAction(chosen int, iran bool) (action int, ok bool) {
	if chosen < 0 {
		return 0, false
	}
	if iran {
		return chosen, true
	}
	return chosen + l3EditActionShift, true
}

// mssClampLabel renders the setting for a menu line, saying what the automatic
// value actually works out to rather than printing a zero.
func mssClampLabel(clamp, mtu int) string {
	switch {
	case clamp == mssClampOffLabel:
		return "off"
	case clamp > 0:
		return fmt.Sprint(clamp) + " bytes"
	default:
		return fmt.Sprintf("automatic (%d bytes, from the MTU)", mtu-40)
	}
}

// mssClampOffLabel mirrors the engine's sentinel. It is repeated rather than
// imported because internal/manage does not otherwise depend on the engine
// package, and one constant is a smaller price than that dependency.
const mssClampOffLabel = -1

// saveDirect writes a changed [direct] config back and restarts the tunnel.
func saveDirect(t Tunnel, d config.DirectConfig) {
	side := sideIran
	if d.ResolvedRole() == "origin" {
		side = sideKharej
	}
	spec := directSpec{
		Name: t.Name, Side: side,
		Transport: orDefault(d.Transport, "tcp"),
		Addr:      d.Addr, Token: d.Token,
		Ports: d.Ports, AcceptUDP: d.AcceptUDP,
		MaxConnections: d.MaxConnections, BandwidthMbps: d.BandwidthMbps,
		Sessions:     d.Sessions,
		Preset:       d.Preset,
		MuxFrameSize: d.MaxFrameSize, MuxReceiveBuffer: d.MaxReceiveBuffer,
		MuxStreamBuffer: d.MaxStreamBuffer, Keepalive: d.Keepalive,
		Nodelay:    d.Nodelay,
		ServerName: d.ServerName,
		ACMEDomain: d.ACMEDomain, ACMEEmail: d.ACMEEmail,
		// Never asked for here, and kept so that changing a port does not
		// delete a certificate or a hand-set timeout.
		TLSCertFile: d.TLSCertFile, TLSKeyFile: d.TLSKeyFile,
		MuxVersion:  d.MuxVersion,
		DialTimeout: d.DialTimeout, RetryInterval: d.RetryInterval,
		MSS: d.MSS,
	}
	applyEdit(t, spec.Render())
}

// saveL3 writes a changed [l3] config back and restarts the tunnel.
func saveL3(t Tunnel, l config.L3Config) {
	applyEdit(t, l3SpecOf(t, l).Render())
}

// l3SpecOf rebuilds the wizard's view of an existing layer-3 config, so an
// edit re-renders the whole file rather than patching it.
func l3SpecOf(t Tunnel, l config.L3Config) l3Spec {
	side := sideIran
	if strings.EqualFold(strings.TrimSpace(l.Mode), "listen") {
		side = sideKharej
	}
	return l3Spec{
		Name: t.Name, Side: side,
		Carrier: orDefault(l.Carrier, "udp"),
		Encap:   "gre", GREKey: l.GREKey,
		Addr: l.Addr, Token: l.Token,
		Iface:   orDefault(l.Iface, "bp0"),
		LocalIP: l.LocalIP, PeerIP: l.PeerIP, MTU: l.MTU,
		SockBuf: l.SockBuf, MSSClamp: l.MSSClamp, AutoMTU: l.AutoMTU,
		FECData: l.FECData, FECParity: l.FECParity,
		Paths:  l.Paths,
		Preset: l.Preset, TxQueueLen: l.TxQueueLen, Qdisc: l.Qdisc,
		Ports: l.Ports, AcceptUDP: l.AcceptUDP,
		MaxConnections: l.MaxConnections, BandwidthMbps: l.BandwidthMbps,
		// Carried whole, so an edit to the MTU does not quietly revert a
		// carrier the operator spent an afternoon tuning.
		Spoof: l.SpoofConfig,
		Pck:   l.PckConfig,
	}
}

// validateRendered parses a config the wizard just built, before it replaces
// the old one. See applyEdit.
func validateRendered(body string) error {
	var check config.Config
	if _, err := toml.Decode(body, &check); err != nil {
		return fmt.Errorf("the edit produced a config that does not parse: %w", err)
	}
	return nil
}

// applyEdit writes the rendered config and restarts the service.
//
// The new file is parsed before it replaces the old one. A config that does
// not decode would leave a tunnel that cannot start and an operator with no
// idea why, and the cost of checking is one parse of a file we just built.
func applyEdit(t Tunnel, body string) {
	var check config.Config
	if _, err := toml.Decode(body, &check); err != nil {
		tui.Error("The edit does not parse: " + err.Error())
		tui.PressEnter()
		return
	}
	if err := app.WriteFileAtomic(app.ConfigPath(t.Name), []byte(body), app.TunnelConfigMode); err != nil {
		tui.Error("Cannot write the config: " + err.Error())
		tui.PressEnter()
		return
	}
	if err := RestartService(t.Service); err != nil {
		tui.Error("Saved, but the restart failed: " + err.Error())
		tui.Warn("Log: journalctl -u " + t.Service + " -n 50")
		tui.PressEnter()
		return
	}
	tui.Success("Saved — Restarted.")
	tui.PressEnter()
}

// spoofCarrierSummary is the one line the edit menu shows for the carrier: what
// the packets look like, and whether the paired half of Stealth is on. Those
// are the two settings that have to agree with the other machine, so they are
// the two worth reading off a menu.
func spoofCarrierSummary(sc config.SpoofConfig) string {
	profile := orDefault(sc.SpoofProfile, "udp")
	if sc.SpoofUplink != "" || sc.SpoofDownlink != "" {
		profile = orDefault(sc.SpoofUplink, profile) + "/" + orDefault(sc.SpoofDownlink, profile)
	}
	source := "unforged"
	switch {
	case len(sc.SpoofSrcPool) > 1:
		source = fmt.Sprintf("%d forged sources", len(sc.SpoofSrcPool))
	case sc.SpoofSrcIP != "":
		source = sc.SpoofSrcIP
	}
	stealth := "Stealth off"
	if spoofStealthOn(sc) {
		stealth = "Stealth on"
	}
	return profile + ", " + source + ", " + stealth
}

// editL3Spoof changes the forged-source carrier of a running tunnel.
//
// Stealth is offered on its own because it is the setting most likely to be
// changed twice: it is the one an operator turns on to see whether a path stops
// dropping the tunnel, and off again when it costs more than it bought. Making
// that a trip through the whole setup would be four screens to flip one flag.
//
// Everything else is the setup screen again rather than a field-by-field
// editor, and deliberately: these settings are paired with the other machine,
// and a screen that walks both ends through the same questions in the same
// order is what keeps a pair in step. It ends in the same summary the setup
// does, which is where a mismatch is caught.
func editL3Spoof(t Tunnel, l config.L3Config) bool {
	onIran := !strings.EqualFold(strings.TrimSpace(l.Mode), "listen")
	there := "kharej"
	if !onIran {
		there = "Iran"
	}

	stealth := "Stealth: Turn On"
	if spoofStealthOn(l.SpoofConfig) {
		stealth = "Stealth: Turn Off"
	}

	switch tui.ChooseOpt("IP Spoofing", []tui.Option{
		{Title: stealth, Desc: "the " + there + " end must match"},
		{Title: "All Questions Again", Desc: "profile, forged source, interface"},
	}) {
	case 0:
		if spoofStealthOn(l.SpoofConfig) {
			clearSpoofStealth(&l.SpoofConfig)
		} else {
			applySpoofStealth(&l.SpoofConfig)
		}
		fmt.Println()
		tui.Warn("Set The " + titleWord(there) + " End The Same Way.")
		tui.PressEnter()
		saveL3(t, l)
		return true
	case 1:
		askSpoofCarrier(&l.SpoofConfig, onIran)
		tui.PressEnter()
		saveL3(t, l)
		return true
	}
	return false
}

// askPeerAddress asks for the other machine's new address. A host alone keeps
// the port the tunnel already uses; host:port changes both. Reported on v1.8.4:
// when the kharej's IP changed, the Edit screen had nowhere to say so, and the
// tunnel had to be deleted and made again.
func askPeerAddress(side, current string) (string, bool) {
	fmt.Println()
	tui.Info("IP Or Domain Keeps The Port; IP:Port Changes Both.")
	raw := tui.PromptDefault("New "+side+" IP Or Domain", addrHost(current, ""))
	addr, err := withPeerHost(current, raw)
	if err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return "", false
	}
	return addr, true
}

// withPeerHost is the address current becomes when the operator types in.
func withPeerHost(current, in string) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", fmt.Errorf("an address is required")
	}
	host, port := in, addrPort(current)
	if h, p, err := net.SplitHostPort(in); err == nil {
		if !validPort(p) {
			return "", fmt.Errorf("%q is not a port", p)
		}
		host, port = h, p
	}
	host = strings.Trim(host, "[]")
	if host == "" || strings.ContainsAny(host, " /") {
		return "", fmt.Errorf("%q is not an IP or a domain", in)
	}
	if port == "" {
		return "", fmt.Errorf("give the port too, as IP:port — the tunnel has none recorded")
	}
	return net.JoinHostPort(host, port), nil
}

// movePeer points an Iran-side l3 tunnel at a new kharej address. A spoof
// carrier also records the kharej's real address, and when that was the old
// host it moves with it — otherwise the forged packets would go on being sent
// to the address the kharej just left.
func movePeer(l *config.L3Config, addr string) {
	old := addrHost(l.Addr, "")
	l.Addr = addr
	if old != "" && l.SpoofPeerIP == old {
		// A domain is left to the default, which is this very address.
		l.SpoofPeerIP = ""
		if ip := net.ParseIP(addrHost(addr, "")); ip != nil {
			l.SpoofPeerIP = ip.String()
		}
	}
}
