package manage

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/optimize"
	"github.com/backpack/backpack/internal/tui"
)

// The direct tunnel wizard.
//
// Nobody should have to write TOML. This asks the questions in the order the
// answers depend on each other, and writes the file itself.
//
// The order matters and is not the reverse wizard's. There, the transport is
// chosen first because every transport works. Here, the answers narrow each
// other: which machine you are on decides which questions are even asked, and
// what kind of tunnel you want decides which transports can carry it — a
// layer-3 tunnel cannot run over a reliable transport at all. Asking in
// dependency order means an impossible combination is never offered, rather
// than offered and then rejected.
//
//	1. which machine — Iran or kharej
//	2. what kind     — forwarded ports, or a full IP tunnel
//	3. how it travels — only the transports that can carry that kind
//	4. the details    — ports, addresses, whatever the choices above imply
//
// Geography is the user-facing name throughout. It is what stays true in both
// directions: Iran exposes the ports either way, and only who dials changes.
// "server" and "client" would be actively misleading here, because in a direct
// tunnel the Iran machine is the one that dials.

// directSide is which machine the wizard is being run on.
type directSide int

const (
	sideIran directSide = iota
	sideKharej
)

func (s directSide) String() string {
	if s == sideIran {
		return "iran"
	}
	return "kharej"
}

// Which machine this is running on is settled by the menu entry the operator
// chose — Setup Iran or Setup Kharej — so nothing here asks it again. See
// setupentry.go for the two ways in.

// ---------------------------------------------------------------- layer 4

// askSharedToken settles the one value both machines must hold identically.
//
// It is asked differently on the two sides, and that asymmetry is the whole
// point. Offering a freshly generated token as the default on both ends means
// somebody sets up the first machine, presses Enter, sets up the second,
// presses Enter again — and now the two tokens differ. Nothing says so: a
// mismatched token is answered with silence by design, so the tunnel looks
// exactly like a blocked port, which is the single most expensive way this can
// go wrong.
//
// So only the listening side offers one. The dialling side is asked for the
// token from the other machine, with no default to accept by reflex — and if
// that machine has not been set up yet, saying so generates one to carry over
// instead, which keeps either order workable without ever silently producing
// two different tokens.
func askSharedToken(side directSide) (string, bool) {
	fmt.Println()

	if side == sideKharej {
		suggested := randomToken(64)
		tui.Info("Suggested 64-character token — press Enter to accept, then copy it")
		tui.Info("to the Iran server. Both ends must use exactly the same token.")
		fmt.Println("  " + tui.Color(tui.Bold+tui.White, suggested))
		token := strings.TrimSpace(tui.PromptDefault("Security token", suggested))
		if token == "" {
			tui.Error("A token is required.")
			tui.PressEnter()
			return "", false
		}
		return token, true
	}

	tui.Info("The kharej server generates the token. Paste it here — it must")
	tui.Info("match exactly, and a token that does not match looks identical to")
	tui.Info("a blocked port, because a wrong one is answered with silence.")
	fmt.Println()
	tui.Warn("Not set up the kharej server yet? Leave this blank and one will be")
	tui.Warn("generated here for you to copy over there instead.")
	token := strings.TrimSpace(tui.Prompt("Token from the kharej server: "))
	if token != "" {
		return token, true
	}

	generated := randomToken(64)
	fmt.Println()
	tui.Info("Generated here instead — copy it to the kharej server:")
	fmt.Println("  " + tui.Color(tui.Bold+tui.White, generated))
	return generated, true
}

// ---------------------------------------------------------------- layer 3

// setupL3 builds an [l3] tunnel: a private network between the two servers.
func setupL3(side directSide) {
	fmt.Println()
	tui.Warn("A full IP tunnel puts both servers on one private network, so they")
	tui.Warn("can reach each other by address and carry anything — not just the")
	tui.Warn("ports you list. It needs root and a Linux kernel with TUN support.")
	fmt.Println()

	carrier, ok := askL3Carrier()
	if !ok {
		return
	}

	// There is nothing to ask about the encapsulation. Every direct tunnel is
	// GRE inside the Noise session — the framing Backpack writes itself, not
	// the kernel's protocol 47 — and offering a choice between that and IPIP
	// was offering four bytes of saving in exchange for one more decision and
	// one more thing the two ends can silently disagree about. The engine still
	// reads "ipip" from a config that has it, so nothing already built breaks.
	const encap = "gre"
	greKey := uint32(0)

	cfg := l3Spec{Side: side, Carrier: carrier, Encap: encap, GREKey: greKey}
	// Chosen against what is already on the machine, so a second tunnel does
	// not land on the first one's subnet. See freeL3Subnet.
	suggestedLocal, suggestedPeer := freeL3Subnet(side)

	// The Iran side dials out, which is the whole point of "direct".
	if side == sideIran {
		host := tui.Prompt("Kharej server address (IP or domain): ")
		if strings.TrimSpace(host) == "" {
			tui.Error("An address is required.")
			tui.PressEnter()
			return
		}
		port := tui.PromptDefault("Tunnel port on the kharej server", "9000")
		if !validPort(port) {
			tui.Error("Invalid port.")
			tui.PressEnter()
			return
		}
		cfg.Addr = net.JoinHostPort(strings.TrimSpace(host), port)
		cfg.LocalIP, cfg.PeerIP = suggestedLocal, suggestedPeer
	} else {
		port := tui.PromptDefault("Tunnel port to listen on", "9000")
		if !validPort(port) {
			tui.Error("Invalid port.")
			tui.PressEnter()
			return
		}
		cfg.Addr = net.JoinHostPort("0.0.0.0", port)
		cfg.LocalIP, cfg.PeerIP = suggestedLocal, suggestedPeer
	}

	fmt.Println()
	tui.Info("Private addresses for the two ends of the tunnel. The defaults are")
	tui.Info("fine unless " + l3Block(cfg.LocalIP) + "x is already used on either machine.")
	tui.Info("Both servers must agree, with the addresses swapped.")
	cfg.LocalIP = tui.PromptDefault("This machine's tunnel address", cfg.LocalIP)
	cfg.PeerIP = tui.PromptDefault("The other machine's tunnel address", cfg.PeerIP)

	// Same order as the reverse wizard and as the direct one above: address,
	// name, token, ports, carrier-specific extras, then optional tuning.
	cfg.Name = uniqueName(tui.PromptDefault("Tunnel name", cfg.defaultName()))

	if !askL3Token(&cfg) {
		return
	}

	// Ports over a layer-3 tunnel are optional: without them it simply routes.
	if side == sideIran {
		fmt.Println()
		tui.Info("Optionally forward ports over the tunnel as well. Leave blank to")
		tui.Info("just have the private network and route traffic yourself.")
		raw := tui.Prompt("Ports to expose here (blank for none): ")
		if strings.TrimSpace(raw) != "" {
			cfg.Ports = parsePorts(raw)
			if err := validatePortSpecs(cfg.Ports); err != nil {
				tui.Error(err.Error())
				tui.PressEnter()
				return
			}
			cfg.AcceptUDP = tui.Confirm("Carry UDP as well as TCP on those ports", false)
		}
	}

	// The forged-source carrier cannot learn where its peer really is, because
	// every packet it receives carries a forged source. This sits where the
	// reverse wizard asks its own transport-specific questions.
	if carrier == "spoof" && side == sideKharej {
		fmt.Println()
		tui.Warn("The spoof carrier forges the source of every packet, so this side")
		tui.Warn("cannot work out where to send replies. It has to be told.")
		cfg.Spoof.SpoofPeerIP = strings.TrimSpace(tui.Prompt("The Iran server's real IP: "))
		if net.ParseIP(cfg.Spoof.SpoofPeerIP) == nil {
			tui.Error("A valid IP address is required for the spoof carrier.")
			tui.PressEnter()
			return
		}
	}

	cfg.MTU, cfg.Iface = defaultL3MTU, freeL3Iface()
	chooseL3Preset().apply(&cfg)
	if tui.Confirm("Fine-tune the advanced settings by hand", false) {
		askL3Advanced(&cfg, side)
	}

	summariseL3(cfg)
	if !tui.Confirm("Create this tunnel", true) {
		return
	}
	writeAndStart(cfg.Name, cfg.render(), cfg.Side, cfg.Token)
}

// Picking an interface and a subnet that are not already taken.
//
// A layer-3 tunnel creates a real network interface and claims a real subnet,
// so two of them cannot have the same of either: the second interface fails to
// come up, and the tunnel restarts every five seconds forever with an error
// only visible in the journal.
//
// This is not a hypothetical second tunnel. The GRE key question a few screens
// earlier exists precisely so that several tunnels can run between the same two
// servers, and tells the operator so — offering them "bp0" and "10.10.0.1/30"
// each time would be handing out a collision on the same screen that suggested
// the arrangement.
//
// Existing tunnels are read rather than guessed at. What is already on the
// machine is the only thing that settles this, and an interface a tunnel is not
// currently running still owns its name the moment it starts.

// eachL3Config visits the [l3] table of every tunnel already set up here.
func eachL3Config(visit func(config.L3Config)) {
	for _, t := range List() {
		cfg, err := LoadTunnelConfig(t.Name)
		if err != nil || !cfg.L3.Enabled() {
			continue
		}
		visit(cfg.L3)
	}
}

// freeL3Iface is the first bpN not already claimed.
func freeL3Iface() string {
	used := map[string]bool{}
	eachL3Config(func(l config.L3Config) { used[orDefault(l.Iface, "bp0")] = true })
	for i := 0; i < 256; i++ {
		name := fmt.Sprintf("bp%d", i)
		if !used[name] {
			return name
		}
	}
	return "bp0"
}

// freeL3Subnet is the two ends of the first 10.10.N.0/30 not already claimed,
// returned in this side's order. The two machines have them the other way
// round, which is what the wizard tells the operator on the next screen.
func freeL3Subnet(side directSide) (localIP, peerIP string) {
	used := map[string]bool{}
	eachL3Config(func(l config.L3Config) {
		// The prefix is what collides, not the individual address, so both ends
		// of an existing tunnel mark the same block.
		used[l3Block(l.LocalIP)] = true
		used[l3Block(l.PeerIP)] = true
	})
	for n := 0; n < 256; n++ {
		block := fmt.Sprintf("10.10.%d.", n)
		if used[block] {
			continue
		}
		if side == sideIran {
			return block + "1/30", block + "2"
		}
		return block + "2/30", block + "1"
	}
	if side == sideIran {
		return "10.10.0.1/30", "10.10.0.2"
	}
	return "10.10.0.2/30", "10.10.0.1"
}

// l3Block reduces an address to the "10.10.N." it belongs to, which is the
// granularity two tunnels can collide at.
func l3Block(addr string) string {
	addr, _, _ = strings.Cut(strings.TrimSpace(addr), "/")
	if idx := strings.LastIndex(addr, "."); idx >= 0 {
		return addr[:idx+1]
	}
	return ""
}

// defaultL3MTU is deliberately low. A tunnel whose packets are slightly too
// big does not fail loudly: small things work and downloads stall. Starting
// under the budget and letting an operator raise it once the tunnel is proven
// is the cheaper mistake.
const defaultL3MTU = 1400

// askL3Advanced is what sits behind "fine-tune by hand", matching where the
// reverse wizard keeps its own. None of it needs answering.
func askL3Advanced(cfg *l3Spec, side directSide) {
	fmt.Println()
	tui.Info("The tunnel measures what the path really carries once it is up and")
	tui.Info("corrects the MTU itself, so this is only a starting point. Turn that")
	tui.Info("off only if you have measured the path yourself and want it fixed.")
	cfg.MTU = tui.PromptInt("Starting MTU", cfg.MTU)
	if !tui.Confirm("Let the tunnel measure and correct the MTU automatically", true) {
		off := false
		cfg.AutoMTU = &off
	}

	cfg.Iface = tui.PromptDefault("Network interface name to create", cfg.Iface)

	fmt.Println()
	tui.Info("A key separates tunnels that share the same two servers. Leave it at")
	tui.Info("0 unless you are running more than one between them, and set the")
	tui.Info("same number on both machines.")
	cfg.GREKey = askGREKey()

	fmt.Println()
	tui.Info("Backpack caps the segment size of TCP crossing the tunnel, which is")
	tui.Info("what stops large transfers stalling when the network drops the ICMP")
	tui.Info("message that would otherwise have told both ends to send less.")
	tui.Info("Leave this at 0 unless a path measurement gave you a number.")
	cfg.MSSClamp = tui.PromptInt("TCP segment cap (0 = from the MTU, -1 = off)", cfg.MSSClamp)

	// Only the forwarded ports can be capped: routed traffic goes through the
	// interface and never passes the forwarder, so there is nothing to count.
	if side == sideIran && len(cfg.Ports) > 0 {
		fmt.Println()
		tui.Info("Both caps are off by default, and cover the forwarded ports only.")
		cfg.MaxConnections = tui.PromptInt("Maximum simultaneous connections (0 = unlimited)", cfg.MaxConnections)
		cfg.BandwidthMbps = tui.PromptInt("Maximum bandwidth in Mbit/s (0 = unlimited)", cfg.BandwidthMbps)
	}
}

// askL3Carrier offers the datagram carriers, in the order they are worth
// trying: pck first, because it survives the most, then plain udp where the
// path allows it, then spoof for a route that needs it.
//
// A layer-3 tunnel carries IP packets, which already belong to something that
// handles its own loss; stacking that on a retransmitting transport makes
// throughput collapse rather than degrade, so tcp, ws and kcp are not offered
// at all.
//
// xdi is no longer offered either. It still runs — the engine reads a config
// that names it, so an existing tunnel keeps working — but ICMP-only routes are
// rare enough that it was one more choice to weigh against three that matter.
func askL3Carrier() (string, bool) {
	switch tui.ChooseOpt("How should the packets travel?", []tui.Option{
		{Title: "PCK", Desc: "looks like an ordinary TCP flow, but with no socket the firewall can touch"},
		{Title: "UDP", Desc: "plain and simple — use it where the path does not interfere"},
		{Title: "Spoof", Desc: "raw packets with a forged source address — needs testing on your route"},
	}) {
	case 0:
		return "pck", true
	case 1:
		return "udp", true
	case 2:
		return "spoof", true
	}
	return "", false
}

// askL3Encap settles how the inner packet is framed.
//
// It is asked plainly because the honest answer is that most people want
// ipip and should not have to think about it: it costs nothing and there is
// nothing to get wrong. GRE earns its four bytes only when the key is wanted,
// so that is what the question is really about.
// askGREKey is the RFC 2890 key, which both GRE kinds offer and mean the same
// thing by: a number that separates tunnels sharing the same two endpoints.
func askGREKey() uint32 {
	for {
		key := tui.PromptInt("Tunnel key (0 for none)", 0)
		if key >= 0 && key <= 4294967295 {
			return uint32(key)
		}
		tui.Error("The key must be between 0 and 4294967295.")
	}
}

func askL3Token(cfg *l3Spec) bool {
	token, ok := askSharedToken(cfg.Side)
	cfg.Token = token
	return ok
}

// ---------------------------------------------------------------- summaries

func summariseDirect(cfg directSpec) {
	fmt.Println()
	tui.Rule()
	tui.Title("About to create")
	tui.Info("Kind        : direct tunnel, forwarded ports")
	tui.Info("This machine: " + sideLabel(cfg.Side))
	tui.Info("Transport   : " + cfg.Transport)
	if cfg.Side == sideIran {
		tui.Info("Dials       : " + cfg.Addr)
		tui.Info("Exposes     : " + strings.Join(cfg.Ports, ", "))
		tui.Info("UDP         : " + onOff(cfg.AcceptUDP))
		tui.Info("Tuning      : " + presetLabel(cfg.Preset) + fmt.Sprintf(", %d session(s)", cfg.Sessions))
		if cfg.MaxConnections > 0 || cfg.BandwidthMbps > 0 {
			tui.Info("Limits      : " + limitsLabel(cfg.MaxConnections, cfg.BandwidthMbps))
		}
	} else {
		tui.Info("Listens on  : " + cfg.Addr)
	}
	tui.Info("Config file : " + app.ConfigPath(cfg.Name))
	tui.Rule()
	fmt.Println()
	remindOtherSide(cfg.Side, cfg.Token)
}

func summariseL3(cfg l3Spec) {
	fmt.Println()
	tui.Rule()
	tui.Title("About to create")
	tui.Info("Kind        : full IP tunnel (layer 3)")
	tui.Info("This machine: " + sideLabel(cfg.Side))
	tui.Info("Carrier     : " + cfg.Carrier)
	encap := cfg.Encap
	if cfg.GREKey != 0 {
		encap += fmt.Sprintf(" (key %d)", cfg.GREKey)
	}
	tui.Info("Wrapping    : " + encap)
	if cfg.Side == sideIran {
		tui.Info("Dials       : " + cfg.Addr)
	} else {
		tui.Info("Listens on  : " + cfg.Addr)
	}
	tui.Info("Interface   : " + cfg.Iface + "  " + cfg.LocalIP + " ↔ " + cfg.PeerIP)
	tui.Info("MTU         : " + fmt.Sprint(cfg.MTU) + "  (measured and corrected once the tunnel is up)")
	tui.Info("Tuning      : " + presetLabel(cfg.Preset) + ", " + cfg.Qdisc +
		fmt.Sprintf(", queue %d", cfg.TxQueueLen))
	if len(cfg.Ports) > 0 {
		tui.Info("Exposes     : " + strings.Join(cfg.Ports, ", "))
	}
	tui.Info("Config file : " + app.ConfigPath(cfg.Name))
	tui.Rule()
	fmt.Println()
	// Spelled out because these three are the values that must be identical on
	// the other machine, and getting one wrong used to produce a tunnel that
	// came up, reported a peer and carried nothing.
	tui.Warn("The other machine must use exactly these three:")
	tui.Warn("  carrier " + cfg.Carrier + "   wrapping " + encap + "   the same token")
	fmt.Println()
	tui.Warn("Once both ends are up, test it with:  ping " + strings.SplitN(cfg.PeerIP, "/", 2)[0])
	fmt.Println()
	remindOtherSide(cfg.Side, cfg.Token)
}

// remindOtherSide says what has to happen on the machine this is not, and
// prints the token so it can be copied.
//
// Printing it here matters more than it looks. The token is offered while it
// is being entered, near the top of a long wizard, and by the time the tunnel
// exists it has scrolled away — leaving an operator who has just been told to
// "use the same token" with no token in front of them. Saying what to do
// without showing what to do it with is how a setup ends in `cat`-ing a config
// file to find the one value the wizard already knew.
func remindOtherSide(side directSide, token string) {
	if side == sideIran {
		tui.Warn("Next: run this wizard on the KHAREJ server, choose Kharej, and give")
		tui.Warn("it this same token.")
	} else {
		tui.Warn("Next: run this wizard on the IRAN server, choose Iran, and give it")
		tui.Warn("this token along with this server's address and the tunnel port above.")
	}
	fmt.Println()
	tui.Info("Token — copy it to the other machine exactly:")
	fmt.Println("  " + tui.Color(tui.Bold+tui.White, token))
	fmt.Println()
}

func sideLabel(s directSide) string {
	if s == sideIran {
		return "Iran (dials out, exposes the ports)"
	}
	return "Kharej (listens, holds the service)"
}

// ---------------------------------------------------------------- writing

// writeAndStart puts the rendered config on disk and brings the service up. It
// deliberately mirrors what finishSetup does for a reverse tunnel, so a direct
// tunnel is managed, backed up and deleted by exactly the same machinery.
func writeAndStart(name, body string, side directSide, token string) {
	tui.Info("Applying system network optimizations...")
	optimize.ApplyQuiet()

	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		tui.Error("Cannot create the config directory: " + err.Error())
		tui.PressEnter()
		return
	}
	if err := os.WriteFile(app.ConfigPath(name), []byte(body), 0644); err != nil {
		tui.Error("Cannot write the config: " + err.Error())
		tui.PressEnter()
		return
	}
	if err := writeUnit(name); err != nil {
		tui.Error("Cannot create the service: " + err.Error())
		tui.PressEnter()
		return
	}
	if err := DaemonReload(); err != nil {
		tui.Error("systemd reload failed: " + err.Error())
		tui.PressEnter()
		return
	}

	service := app.ServiceName(name)
	if err := StartService(service); err != nil {
		tui.Error("The tunnel was created but would not start: " + err.Error())
		tui.Warn("Check the log with:  journalctl -u " + service + " -n 50")
		tui.PressEnter()
		return
	}

	fmt.Println()
	if IsActive(service) {
		tui.Success(fmt.Sprintf("Tunnel %q is up and running (%s).", name, service))
	} else {
		tui.Warn(fmt.Sprintf("Tunnel %q created but not active yet — check the log:", name))
		tui.Warn("  journalctl -u " + service + " -n 50")
	}

	// Repeated here, after the tunnel exists, because this is the moment the
	// operator turns to the other machine — and the token they need has been
	// off the screen since the middle of the wizard.
	//
	// A kernel GRE tunnel has no token at all, and printing an empty one under
	// "copy this exactly" would be worse than saying nothing.
	if token != "" {
		fmt.Println()
		tui.Rule()
		remindOtherSide(side, token)
	}

	tui.PressEnter()
}
