package manage

import (
	"fmt"
	"net"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/tunnel/l3"
)

// Editing a direct tunnel after it exists.
//
// The things an operator actually comes back to change are the forwarded
// ports, whether UDP rides along, the MTU — and, since v2.0.0 took the udp and
// quic carriers out, the carrier of a tunnel built on one of them. Anything
// that has to match the other machine is either shown and not offered, or
// offered with a warning to change it there too.
//
// Edits go through the parsed config and are written back through the same
// renderer the wizard uses, so an edited file looks exactly like a fresh one
// and keeps its comments.

// editDirectMenu is the per-tunnel editor for a direct tunnel.
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

		if !cfg.L3.Enabled() {
			showRemovedDirect(cfg.Direct)
			return
		}
		if !editL3Ports(t, cfg) {
			return
		}
	}
}

// showRemovedDirect is all the editor has for a [direct] stream tunnel, which
// v2.0.0 removed: what it was, so the operator can rebuild it, and why it
// cannot run. There is nothing left to edit it into — Delete is on the menu
// above, and Setup → Direct builds its replacement.
func showRemovedDirect(d config.LegacyDirectConfig) {
	side := "Iran"
	if !d.OnIran() {
		side = "Kharej"
	}
	tui.Info("Kind         : Direct stream tunnel (removed in v2.0.0)")
	tui.Info("This Server  : " + side)
	tui.Info("Transport    : " + orDefault(d.Transport, "tcp"))
	tui.Info("Address      : " + d.Addr)
	if len(d.Ports) > 0 {
		tui.Info("Ports        : " + strings.Join(d.Ports, ", "))
	}
	fmt.Println()
	tui.Warn(config.ErrDirectRemoved.Error() + ".")
	tui.PressEnter()
}

// editL3Ports does the same for an [l3] tunnel.
func editL3Ports(t Tunnel, cfg config.Config) bool {
	l := cfg.L3
	iran := !strings.EqualFold(strings.TrimSpace(l.Mode), "listen")
	if strings.EqualFold(strings.TrimSpace(l.Carrier), l3.CarrierPck) {
		return editPck(t, l)
	}

	tui.Info("Kind         : Direct (Layer 3)")
	tui.Info("This Server  : " + l3Role(l.Mode))
	tui.Info("Carrier      : " + carrierLabel(orDefault(l.Carrier, "udp")))
	tui.Info("Wrapping     : " + l3EncapLabel(l))
	tui.Info("Address      : " + l.Addr)
	tui.Info("Interface    : " + orDefault(l.Iface, "bp0") + "  " + l.LocalIP + " ↔ " + l.PeerIP)
	if l3.KernelCarrier(l.Carrier) && l.MTU == 0 {
		tui.Info("MTU          : from the interface, less GRE's own")
	} else {
		tui.Info("MTU          : " + fmt.Sprint(l.MTU))
	}
	tui.Info("Tuning       : " + presetLabel(l.Preset) + ", " + orDefault(l.Qdisc, "fq_codel"))
	if len(l.Ports) > 0 {
		tui.Info("Ports        : " + strings.Join(l.Ports, ", "))
		tui.Info("UDP          : " + onOff(l.AcceptUDP))
	}
	fmt.Println()
	// A tunnel on a carrier v2.0.0 took out cannot start, and every other edit
	// here would restart it into the same refusal. Said first, with the one
	// entry that fixes it.
	if l3.RemovedCarrier(l.Carrier) {
		tui.Warn("Cannot Start: The " + strings.ToUpper(orDefault(l.Carrier, "udp")) +
			" Carrier Was Removed In v2.0.0. Choose Carrier Below — The Same On Both Servers.")
		fmt.Println()
	}

	// A GRE link has no token; what the other end has to match is its key, so
	// that is what the same entry shows.
	kernel := l3.KernelCarrier(l.Carrier)
	secret := tui.Option{Title: "Show Token", Desc: ""}
	mtuNow := fmt.Sprint(l.MTU)
	if kernel {
		secret = tui.Option{Title: "Show GRE Key", Desc: "now " + fmt.Sprint(l.GREKey)}
		if l.MTU == 0 {
			mtuNow = "from the interface"
		}
		tui.Warn("Not Encrypted: Kernel GRE Is Bare IP Protocol 47.")
		fmt.Println()
	}
	options := []tui.Option{
		{Title: "MTU", Desc: "now " + mtuNow},
		{Title: "TCP MSS Clamp", Desc: "now " + mssClampLabel(l.MSSClamp, l.MTU)},
		secret,
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
	case kernel:
		// Both ends of a GRE link name the other's real address.
		peer = len(options)
		other := "Kharej Address"
		if !iran {
			other = "Iran Address"
		}
		options = append(options, tui.Option{Title: other, Desc: "now " + l.Addr})
	case iran:
		peer = len(options)
		options = append(options, tui.Option{Title: "Kharej Address", Desc: "now " + l.Addr})
	case strings.EqualFold(strings.TrimSpace(l.Carrier), "spoof"):
		peer = len(options)
		options = append(options, tui.Option{Title: "Iran Real IP",
			Desc: "now " + orDefault(l.SpoofPeerIP, "not set")})
	}

	// The carrier, last of all and outside l3EditAction's range like the peer
	// address above it. It has to match the other machine, so it was left
	// out of this screen while every carrier still ran; a tunnel on one of the
	// removed ones has no other way back short of being built again.
	//
	// Not on a GRE tunnel: a datagram carrier needs a token and a port the GRE
	// link never had, so moving between the two is a new tunnel, not an edit.
	carrierAt := -1
	if !kernel {
		carrierAt = len(options)
		options = append(options, tui.Option{Title: "Carrier",
			Desc: "now " + carrierLabel(orDefault(l.Carrier, "udp")) + " — set the other server the same"})
	}

	chosen := tui.ChooseOpt("Edit", options)
	if chosen >= 0 && chosen == carrierAt {
		if spec, ok := askNewL3Carrier(t, l); ok {
			applyEdit(t, spec.Render())
		}
		return true
	}
	if chosen >= 0 && chosen == peer {
		if kernel {
			if !askGREPeerAddress(&l, iran) {
				return true
			}
			saveL3(t, l)
			return true
		}
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
		label := "Tunnel MTU (Lower It If Downloads Stall)"
		if kernel {
			label = "Tunnel MTU (0 = From The Interface, Less GRE's Own)"
		}
		l.MTU = tui.PromptInt(label, l.MTU)
		saveL3(t, l)
	case 3:
		fmt.Println()
		l.MSSClamp = tui.PromptInt("TCP MSS Clamp (0 = From MTU, -1 = Off)", l.MSSClamp)
		saveL3(t, l)
	case 4:
		fmt.Println()
		if kernel {
			tui.Info("GRE Key (The Same On Both Servers):")
			fmt.Println("  " + tui.Color(tui.Bold+tui.White, fmt.Sprint(l.GREKey)))
			tui.PressEnter()
			break
		}
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

// askNewL3Carrier asks which carrier the tunnel should move to, and the
// questions that carrier has — the SNI domain, the spoof screen — exactly as
// Setup asks them, then returns the whole tunnel re-rendered onto it. It
// writes nothing: false means the operator went back or the answers cannot
// work, and the file is left as it was.
//
// It starts from the config as it is, whatever its carrier. A udp or quic
// tunnel does not validate any more, and this is how it is fixed, so nothing
// on the way here may refuse to read one.
func askNewL3Carrier(t Tunnel, l config.L3Config) (l3Spec, bool) {
	carrier, ok := askL3Carrier()
	if !ok {
		return l3Spec{}, false
	}
	// GRE is a link with no port and no token, naming both servers' real
	// addresses; nothing in this tunnel's file says what those would be.
	if l3.KernelCarrier(carrier) != l3.KernelCarrier(l.Carrier) {
		tui.Error("A GRE Tunnel Is Set Up Anew, Not Changed Into: Setup Iran → Direct → GRE, Then Delete This One.")
		tui.PressEnter()
		return l3Spec{}, false
	}
	spec := directSpecFrom(t.Name, l)
	spec.Carrier = carrier
	if !askL3CarrierExtras(&spec, spec.Side) {
		return l3Spec{}, false
	}
	if err := validL3Spec(spec); err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return l3Spec{}, false
	}
	fmt.Println()
	tui.Warn("Set The Other Server To " + carrierLabel(carrier) + " As Well — The Two Ends Must Match.")
	return spec, true
}

// validL3Spec renders a spec, reads it back the way the engine will, and asks
// the engine whether it would start — so an edit is refused here, with the
// engine's reason, rather than written and then refused at the restart.
func validL3Spec(spec l3Spec) error {
	var cfg config.Config
	if _, err := toml.Decode(spec.Render(), &cfg); err != nil {
		return fmt.Errorf("the edit produced a config that does not parse: %w", err)
	}
	engine, err := l3.ConfigFromTable(cfg.L3)
	if err != nil {
		return err
	}
	return engine.Validate()
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

// saveL3 writes a changed [l3] config back and restarts the tunnel.
func saveL3(t Tunnel, l config.L3Config) {
	applyEdit(t, directSpecFrom(t.Name, l).Render())
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

// askGREPeerAddress points a GRE tunnel at the other server's new address. A
// GRE link has no port, and the family of the new address decides the link: a
// move from IPv4 to IPv6 is a move from gre to ip6gre, which the other end has
// to follow, and the screen says so.
func askGREPeerAddress(l *config.L3Config, iran bool) bool {
	other := "Kharej"
	if !iran {
		other = "Iran"
	}
	fmt.Println()
	host := strings.Trim(strings.TrimSpace(tui.PromptDefault("New "+other+" IP Or Domain", l.Addr)), "[]")
	if host == "" {
		tui.Error("An address is required.")
		tui.PressEnter()
		return false
	}
	carrier, err := greCarrierForHost(host, "")
	if err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return false
	}
	if carrier != l.Carrier {
		tui.Warn("That Moves The Tunnel To " + greUnderlayLabel(carrier) + " (" + carrier +
			") — Set The Other Server's End The Same Way.")
		// A local address of the old family cannot name an end of the new link.
		l.GRELocal = ""
	}
	l.Addr, l.Carrier = host, carrier
	return true
}
