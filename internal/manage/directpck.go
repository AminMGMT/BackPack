package manage

import (
	"fmt"
	"net"
	"strings"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/tunnel/l3"
	"github.com/backpack/backpack/internal/tunnel/paqet"
)

// Setting up a PCK tunnel.
//
// PCK is a port-forwarding transport since v2.0.0 (internal/tunnel/paqet): no
// TUN device, no tunnel addresses, no MTU to measure, no error correction.
// What there is to ask is what every other transport asks — where the kharej is,
// which port, which ports to forward, UDP or not, and a preset — and nothing
// else. Everything the engine can find out, it finds out at start: the network
// interface, this server's address, the gateway's hardware address and the
// firewall rule that keeps the kernel from answering the tunnel's own packets.
//
// The token is not asked on the Iran side. It is the encryption key, so it is
// generated here, shown in the summary, and carried to the kharej in the setup
// link; typing a key twice is the one step that goes wrong silently.
//
// Questions, Iran:
//
//	Kharej IP or domain, tunnel port, forwarded ports, UDP too?, tunnel name,
//	preset (shown with what it means), fine-tune? (no), restart schedule?
//
// and Kharej, by hand: tunnel port, tunnel name, token, preset, fine-tune?.
// With the setup link, the kharej is asked only for a name.

// pckPresetOptions are the three presets as the screens offer them, each with
// what it sets in the engine's own words, in the order Turbo, Balance,
// Aggressive that every other carrier uses.
func pckPresetOptions() []tui.Option {
	desc := func(name, lead string) tui.Option {
		p, _ := paqet.PresetParams(name)
		return tui.Option{Title: strings.ToUpper(name[:1]) + name[1:], Desc: lead + p.Summary()}
	}
	return []tui.Option{
		desc(paqet.PresetTurbo, "default — "),
		desc(paqet.PresetBalance, "least memory — "),
		desc(paqet.PresetAggressive, "long, lossy routes, needs RAM; slower than Turbo on a clean path — "),
	}
}

// choosePckPreset asks which preset, by what it sets.
func choosePckPreset() string {
	fmt.Println()
	switch tui.ChooseOpt("How Should The Tunnel Be Tuned?", pckPresetOptions()) {
	case 1:
		return paqet.PresetBalance
	case 2:
		return paqet.PresetAggressive
	}
	return paqet.PresetTurbo
}

// pckParamsOf is what the engine would run a spec with, so a screen can show
// the preset's numbers next to the ones asked for.
func pckParamsOf(preset string) paqet.Params {
	p, _ := paqet.PresetParams(preset)
	return p.Resolve(preset)
}

// askPckFineTune is "fine-tune" for a PCK tunnel: the six pck_* keys, each
// blank for "from the preset", and on the Iran side the two limits. Nothing in
// it needs answering. It reports false when an answer cannot work.
func askPckFineTune(cfg *l3Spec, iran bool) bool {
	base := pckParamsOf(cfg.Preset)
	fmt.Println()
	tui.Info("Every Answer Is Optional: 0 Or Enter Keeps What The Preset Sets.")
	if iran {
		fmt.Println()
		cfg.PckConns = zeroIf(tui.PromptInt(fmt.Sprintf("KCP Sessions (Preset: %d, At Most %d)", base.Conns, paqet.MaxConns), cfg.PckConns), base.Conns)
	}
	fmt.Println()
	modes := []string{paqet.ModeNormal, paqet.ModeFast, paqet.ModeFast2, paqet.ModeFast3}
	opts := []tui.Option{
		{Title: "From The Preset", Desc: "now " + base.Mode},
		{Title: "Normal", Desc: "least CPU"},
		{Title: "Fast", Desc: "balanced"},
		{Title: "Fast2", Desc: "quicker retransmits"},
		{Title: "Fast3", Desc: "lowest latency, most CPU"},
	}
	if i := tui.ChooseOpt("KCP Speed Mode", opts); i > 0 && i <= len(modes) {
		cfg.PckKCPMode = zeroStr(modes[i-1], base.Mode)
	}
	fmt.Println()
	cfg.PckKCPSndWnd = zeroIf(tui.PromptInt(fmt.Sprintf("Send Window, Packets (Preset: %d)", base.SndWnd), cfg.PckKCPSndWnd), base.SndWnd)
	cfg.PckKCPRcvWnd = zeroIf(tui.PromptInt(fmt.Sprintf("Receive Window, Packets (Preset: %d)", base.RcvWnd), cfg.PckKCPRcvWnd), base.RcvWnd)
	cfg.PckKCPMTU = zeroIf(tui.PromptInt(fmt.Sprintf("KCP Packet Size (%d To %d, Preset: %d)", paqet.MinMTU, paqet.MaxMTU, base.MTU), cfg.PckKCPMTU), base.MTU)
	fmt.Println()
	tui.Info("The Cipher Must Be The Same On Both Servers.")
	cfg.PckKCPBlock = zeroStr(strings.ToLower(tui.PromptDefault("Cipher ("+strings.Join(pckCommonBlocks, ", ")+", …)", orDefault(cfg.PckKCPBlock, paqet.DefaultBlock))), paqet.DefaultBlock)
	if iran && len(cfg.Ports) > 0 {
		fmt.Println()
		cfg.MaxConnections = tui.PromptInt("Max Connections (0 = No Limit)", cfg.MaxConnections)
		cfg.BandwidthMbps = tui.PromptInt("Bandwidth Mbit/s (0 = No Limit)", cfg.BandwidthMbps)
	}
	if err := validL3Spec(*cfg); err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return false
	}
	return true
}

// pckCommonBlocks are the ciphers worth naming on a prompt; the engine accepts
// the longer list in paqet.Blocks.
var pckCommonBlocks = []string{"aes", "aes-128-gcm", "salsa20", "none"}

// zeroIf is v, or 0 when v is what the preset already says: a key that repeats
// its default is one the next preset change would not move.
func zeroIf(v, preset int) int {
	if v == preset {
		return 0
	}
	return v
}

func zeroStr(v, preset string) string {
	if v == preset {
		return ""
	}
	return v
}

// setupPck is the wizard for one side of a PCK tunnel set up by hand: the whole
// of the Iran side, and the kharej side when there is no setup link.
func setupPck(side directSide) { setupPckAsking(side, false) }

// setupPckAsking is the wizard; skipPcap leaves out the libpcap check, for a
// test that walks the questions.
func setupPckAsking(side directSide, skipPcap bool) {
	fmt.Println()
	if !skipPcap && !paqet.PcapAvailable {
		tui.Error(paqet.ErrNoPcap.Error())
		tui.PressEnter()
		return
	}
	cfg := l3Spec{Side: side, Carrier: l3.CarrierPck, Encap: "gre"}

	if side == sideIran {
		host := strings.Trim(strings.TrimSpace(tui.Prompt("Kharej IP Or Domain: ")), "[]")
		if host == "" {
			tui.Error("An address is required.")
			tui.PressEnter()
			return
		}
		if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
			tui.Error("PCK carries IPv4 only: give the kharej server's IPv4 address or a domain.")
			tui.PressEnter()
			return
		}
		port := tui.PromptDefault("Tunnel Port", "9000")
		if !validPort(port) {
			tui.Error("Invalid port.")
			tui.PressEnter()
			return
		}
		cfg.Addr = net.JoinHostPort(host, port)
		raw := tui.Prompt("Forwarded Ports (e.g. 443, 2053=8443): ")
		cfg.Ports = parsePorts(raw)
		if len(cfg.Ports) == 0 {
			tui.Error("PCK forwards ports and nothing else: give at least one.")
			tui.PressEnter()
			return
		}
		if err := validatePortSpecs(cfg.Ports); err != nil {
			tui.Error(err.Error())
			tui.PressEnter()
			return
		}
		if busy := busyForwardPorts(cfg.Ports, ""); len(busy) > 0 {
			tui.Error("Already in use on this server: " + strings.Join(busy, ", ") +
				" — the web panel's own port is the usual one. Pick other ports.")
			tui.PressEnter()
			return
		}
		cfg.AcceptUDP = tui.Confirm("Carry UDP As Well As TCP On Those Ports", false)
		// The key, made here: both ends must hold the same one, and the setup
		// link carries it across.
		cfg.Token = randomToken(64)
	} else {
		port := tui.PromptDefault("Tunnel Port", "9000")
		if !validPort(port) {
			tui.Error("Invalid port.")
			tui.PressEnter()
			return
		}
		if other := pckPortOwner(port); other != "" {
			tui.Error("Tunnel " + other + " on this server already listens for PCK on port " + port +
				" — two tunnels cannot share one. Pick another.")
			tui.PressEnter()
			return
		}
		if portHeld(":" + port) {
			tui.Warn("Something On This Server Already Listens On Port " + port + " — The Tunnel Needs It Free.")
		}
		cfg.Addr = net.JoinHostPort("0.0.0.0", port)
		cfg.Token = strings.TrimSpace(tui.Prompt("Security Token (From The Iran Server): "))
		if cfg.Token == "" {
			fmt.Println()
			tui.Error("Set up the Iran server first — its setup link fills this side in.")
			tui.PressEnter()
			return
		}
	}
	cfg.Name = uniqueName(tui.PromptDefault("Tunnel Name", cfg.DefaultName()))

	cfg.Preset = choosePckPreset()
	if tui.Confirm("Fine-Tune The Advanced Settings", false) {
		if !askPckFineTune(&cfg, side == sideIran) {
			return
		}
	}

	link := ""
	var extras linkExtras
	if side == sideIran {
		extras.restartHours, extras.restartMinute = askScheduledRestart()
		link = pendingShareLinkFrom(cfg, "", extras)
	}
	if err := validL3Spec(cfg); err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	summarisePck(cfg, link)
	if extras.restartHours > 0 {
		tui.Info("Scheduled Restart: " + scheduleLabel(extras.restartHours, extras.restartMinute))
	}
	if !tui.Confirm("Create This Tunnel", true) {
		return
	}
	if writeAndStart(cfg.Name, cfg.Render(), cfg.Side, cfg.Token, link != "") {
		applyLinkExtras(cfg.Name, extras)
		tui.PressEnter()
	}
}

// summarisePck is the screen before "Create This Tunnel", with no interface and
// no address to show: what it does, where it goes, how it is tuned.
func summarisePck(cfg l3Spec, link string) {
	row := func(label, value string) {
		fmt.Printf("%s%-16s:%s %s\n", tui.Gray, label, tui.Reset, tui.Color(tui.White, value))
	}
	fmt.Println()
	tui.Rule()
	tui.Title("Direct PCK")
	fmt.Println()
	if cfg.Side == sideIran {
		row("Dials", cfg.Addr)
		ports := strings.Join(cfg.Ports, ", ")
		if cfg.AcceptUDP {
			ports += "  (TCP + UDP)"
		}
		row("Forwarded Ports", ports)
	} else {
		row("Listens On", cfg.Addr)
	}
	row("Tuning", presetLabel(cfg.Preset)+" — "+pckParamsFor(cfg).Summary())
	if cfg.Side == sideIran {
		row("Token", cfg.Token+"  (the encryption key; the link carries it)")
	}
	row("Config File", app.ConfigPath(cfg.Name))
	if link != "" {
		printLinkBlock(link, "sudo backpack → Setup Kharej → Direct → Setup Link")
	}
	tui.Rule()
	fmt.Println()
}

// pckParamsFor is the parameters a spec runs with: its preset, then its own
// pck_* values over it.
func pckParamsFor(cfg l3Spec) paqet.Params {
	c, err := l3.PckEngineConfig(l3.Config{
		Mode: cfg.Side.String(), Preset: cfg.Preset, SockBuf: cfg.SockBuf,
		PckConns: cfg.PckConns, PckKCPMode: cfg.PckKCPMode, PckKCPMTU: cfg.PckKCPMTU,
		PckKCPSndWnd: cfg.PckKCPSndWnd, PckKCPRcvWnd: cfg.PckKCPRcvWnd, PckKCPBlock: cfg.PckKCPBlock,
	})
	if err != nil {
		return pckParamsOf(cfg.Preset)
	}
	return c.Params
}

// pckSpec is NewDirectTunnel.spec for a PCK tunnel: what the panel, a setup
// link and a managed node all build one from. It refuses what the wizard
// refuses, in the same words.
//
// What it does not read is the TUN's: the two tunnel addresses, the MTU, error
// correction and the GRE key mean nothing to this engine. A form or a link from
// a build that still had them carries them along harmlessly, so they are
// dropped rather than refused — refusing would break applying the setup link of
// an Iran server that has not been updated yet.
func (n NewDirectTunnel) pckSpec(side directSide) (l3Spec, error) {
	port := strings.TrimSpace(n.TunnelPort)
	if !validPort(port) {
		return l3Spec{}, fmt.Errorf("tunnel port %q is not a port between 1 and 65535", n.TunnelPort)
	}
	spec := l3Spec{Side: side, Carrier: l3.CarrierPck, Encap: "gre"}
	spec.Addr = net.JoinHostPort("0.0.0.0", port)
	if side == sideIran {
		host := strings.Trim(strings.TrimSpace(n.PeerAddr), "[]")
		if host == "" {
			return l3Spec{}, fmt.Errorf("the kharej server's address is required on the Iran side")
		}
		spec.Addr = net.JoinHostPort(host, port)
	}
	name := strings.TrimSpace(n.Name)
	if name == "" {
		name = spec.DefaultName()
	}
	if !validName(name) {
		return l3Spec{}, fmt.Errorf("tunnel name %q may use only letters, digits, dots and dashes", name)
	}
	spec.Name = uniqueName(name)

	if side == sideKharej {
		if other := pckPortOwner(port); other != "" {
			return l3Spec{}, fmt.Errorf("tunnel %s on this server already listens for PCK on tunnel port %s: "+
				"two tunnels cannot share a port, so give this one another", other, port)
		}
	}

	spec.Token = strings.TrimSpace(n.Token)
	if spec.Token == "" {
		if side == sideKharej {
			return l3Spec{}, fmt.Errorf("a token is required, and must match the Iran server's exactly: " +
				"it is the encryption key, and it is in its setup link")
		}
		spec.Token = randomToken(64)
	}

	preset := strings.ToLower(strings.TrimSpace(n.Preset))
	if preset == "" {
		preset = paqet.PresetTurbo
	}
	if _, ok := paqet.PresetParams(preset); !ok {
		return l3Spec{}, fmt.Errorf("preset %q is not one of %s", n.Preset, strings.Join(paqet.Presets(), ", "))
	}
	spec.Preset = preset

	spec.PckConns, spec.PckKCPMode = n.PckConns, strings.ToLower(strings.TrimSpace(n.PckKCPMode))
	spec.PckKCPMTU, spec.PckKCPSndWnd, spec.PckKCPRcvWnd = n.PckKCPMTU, n.PckKCPSndWnd, n.PckKCPRcvWnd
	spec.PckKCPBlock = strings.ToLower(strings.TrimSpace(n.PckKCPBlock))
	spec.MaxConnections, spec.BandwidthMbps = n.MaxConnections, n.BandwidthMbps

	if side == sideIran {
		spec.Ports = parsePorts(n.Ports)
		if len(spec.Ports) == 0 {
			return l3Spec{}, fmt.Errorf("at least one forwarded port is required on the Iran side")
		}
		if err := validatePortSpecs(spec.Ports); err != nil {
			return l3Spec{}, err
		}
		spec.AcceptUDP = n.AcceptUDP
	}
	if err := validL3Spec(spec); err != nil {
		return l3Spec{}, err
	}
	return spec, nil
}

// PckModes and PckBlocks are the KCP speed modes and ciphers a PCK tunnel's
// file may name, for the panel's fine-tune menus. "manual" is a mode only the
// engine's own tests use, and is not offered.
func PckModes() []string {
	var out []string
	for _, m := range paqet.Modes() {
		if m != paqet.ModeManual {
			out = append(out, m)
		}
	}
	return out
}

// PckBlocks is paqet.Blocks, with the default first.
func PckBlocks() []string {
	out := []string{paqet.DefaultBlock}
	for _, b := range paqet.Blocks() {
		if b != paqet.DefaultBlock {
			out = append(out, b)
		}
	}
	return out
}

// pckRunnable refuses to create a PCK tunnel with a build that cannot run it. A
// tunnel written anyway would restart every few seconds on a log line nobody
// reads; the same refusal is the first thing the wizard says.
func pckRunnable(carrier string) error {
	if strings.EqualFold(strings.TrimSpace(carrier), l3.CarrierPck) && !paqet.PcapAvailable {
		return paqet.ErrNoPcap
	}
	return nil
}

// setupPckForTest is setupPck with the libpcap refusal taken out, so the
// questions can be walked on a machine whose build has none.
func setupPckForTest(side directSide) { setupPckAsking(side, true) }

// pckParamsOfTable is what the engine runs a file with.
func pckParamsOfTable(l config.L3Config) paqet.Params { return pckParamsFor(directSpecFrom("", l)) }

// editPck is the editor for a PCK tunnel: Edit's screen with none of the TUN's
// rows — no interface, no MTU, no MSS clamp — and the engine's own in their
// place. Like the panel's, it writes through directSpecFrom and the renderer, so
// a tunnel edited here and one edited there end up as the same file; and a file
// a previous version wrote is re-rendered as a PCK file, which is when its dead
// TUN keys go.
//
// The carrier is not offered. PCK is not edited into the TUN carriers, nor they
// into it (applyDirectEdit says why): it is set up anew on both servers.
func editPck(t Tunnel, l config.L3Config) bool {
	iran := !strings.EqualFold(strings.TrimSpace(l.Mode), "listen")
	tui.Info("Kind         : Direct (PCK)")
	tui.Info("This Server  : " + l3Role(l.Mode))
	tui.Info("Carries      : " + l3EncapLabel(l))
	tui.Info("Address      : " + l.Addr)
	tui.Info("Tuning       : " + presetLabel(orDefault(l.Preset, PresetTurbo)) + " — " + pckParamsOfTable(l).Summary())
	if len(l.Ports) > 0 && iran {
		tui.Info("Ports        : " + strings.Join(l.Ports, ", "))
		tui.Info("UDP          : " + onOff(l.AcceptUDP))
	}
	if c, err := l3.ConfigFromTable(l); err == nil {
		for _, n := range l3.PckNotes(c) {
			tui.Warn(n)
		}
	}
	fmt.Println()

	type entry struct {
		opt tui.Option
		run func() bool
	}
	var entries []entry
	add := func(title, desc string, run func() bool) {
		entries = append(entries, entry{tui.Option{Title: title, Desc: desc}, run})
	}
	if iran {
		add("Forwarded Ports", "", func() bool {
			raw := tui.Prompt("Forwarded Ports (At Least One): ")
			ports := parsePorts(raw)
			if len(ports) == 0 {
				tui.Error("PCK forwards ports and nothing else: at least one is needed.")
				tui.PressEnter()
				return true
			}
			if err := validatePortSpecs(ports); err != nil {
				tui.Error(err.Error())
				tui.PressEnter()
				return true
			}
			l.Ports = ports
			saveL3(t, l)
			return true
		})
		add("UDP Forwarding", "now "+onOff(l.AcceptUDP), func() bool {
			l.AcceptUDP = tui.Confirm("Carry UDP As Well As TCP", l.AcceptUDP)
			saveL3(t, l)
			return true
		})
	}
	add("Preset", "now "+presetLabel(orDefault(l.Preset, PresetTurbo))+" — resets the fine-tuning to the preset's", func() bool {
		preset := choosePckPreset()
		if err := applyPckEdit(&l, DirectEdit{Preset: &preset}); err != nil {
			tui.Error(err.Error())
			tui.PressEnter()
			return true
		}
		saveL3(t, l)
		return true
	})
	add("Fine-Tune", "sessions, speed mode, windows, packet size, cipher", func() bool {
		spec := directSpecFrom(t.Name, l)
		if !askPckFineTune(&spec, iran) {
			return true
		}
		applyEdit(t, spec.Render())
		return true
	})
	add("Show Token", "the encryption key", func() bool {
		fmt.Println()
		tui.Info("Token (Copy Exactly):")
		fmt.Println("  " + tui.Color(tui.Bold+tui.White, l.Token))
		tui.PressEnter()
		return true
	})
	if iran {
		add("Kharej Address", "now "+l.Addr, func() bool {
			addr, ok := askPeerAddress("Kharej", l.Addr)
			if !ok {
				return true
			}
			movePeer(&l, addr)
			saveL3(t, l)
			return true
		})
	}

	opts := make([]tui.Option, len(entries))
	for i, e := range entries {
		opts[i] = e.opt
	}
	chosen := tui.ChooseOpt("Edit", opts)
	if chosen < 0 || chosen >= len(entries) {
		return false
	}
	return entries[chosen].run()
}

// pckPortOwner names the PCK tunnel on this machine that already listens on the
// tunnel port, or "". Two cannot: the listening side claims the port for itself,
// and the second would never start.
func pckPortOwner(port string) string {
	for _, t := range List() {
		cfg, err := LoadTunnelConfig(t.Name)
		if err != nil || !cfg.L3.Enabled() || cfg.L3.Carrier != l3.CarrierPck {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(cfg.L3.Mode), "listen") && addrPort(cfg.L3.Addr) == port {
			return t.Name
		}
	}
	return ""
}
