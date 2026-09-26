package manage

import (
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/optimize"
	"github.com/backpack/backpack/internal/snispoof"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/tunnel/l3"
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
// So only one side offers one, and it is the Iran side: that is where the
// tunnel is set up first, since it is the side that knows the kharej's address,
// and it hands the token over inside the setup link it prints (sharelink.go). The
// kharej, set up by hand, is asked for the Iran server's token with no default
// to accept by reflex.
func askSharedToken(side directSide) (string, bool) {
	fmt.Println()

	if side == sideIran {
		// Enter takes the generated one; the setup link carries it to kharej.
		token := strings.TrimSpace(tui.PromptDefault("Security Token", randomToken(64)))
		if token == "" {
			tui.Error("A token is required.")
			tui.PressEnter()
			return "", false
		}
		return token, true
	}

	// No default here: a token that does not match looks identical to a
	// blocked port, because a wrong one is answered with silence.
	token := strings.TrimSpace(tui.Prompt("Security Token (From The Iran Server): "))
	if token == "" {
		fmt.Println()
		tui.Error("Set up the Iran server first: it makes the token, and a setup link")
		tui.Error("that fills in this whole side for you.")
		tui.PressEnter()
		return "", false
	}
	return token, true
}

// ---------------------------------------------------------------- layer 3

// setupL3 builds an [l3] tunnel: a private network between the two servers.
func setupL3(side directSide) {
	fmt.Println()
	carrier, ok := askL3Carrier()
	if !ok {
		return
	}
	// IP and SNI spoofing keep the wizard they had: both ends set up by hand,
	// the token made on kharej. Their answers depend on the route, and the
	// operator wanted them left as they were. See directsetup_classic.go.
	if carrier == "spoof" || carrier == "sni" {
		setupL3Classic(side, carrier)
		return
	}

	// The Iran side decides everything the two ends must agree on and hands
	// it over as one setup link (see sharelink.go). Typing it in by hand stays
	// possible, for a code that cannot be carried across, but it is the
	// second choice rather than the only one.
	if side == sideKharej {
		switch tui.ChooseOpt("How Do You Want To Set Up This Side?", []tui.Option{
			{Title: "Setup Link", Desc: "recommended — paste the link the Iran server printed; everything is filled in"},
			{Title: "Manual", Desc: "type the port, the tunnel addresses and the token yourself"},
		}) {
		case 0:
			setupL3FromLink(carrier)
			return
		case 1:
		default:
			return
		}
	}

	// There is nothing to ask about the encapsulation. Every direct tunnel is
	// GRE inside the Noise session — the framing Backpack writes itself, not
	// the kernel's protocol 47 — and offering a choice between that and IPIP
	// was offering four bytes of saving in exchange for one more decision and
	// one more thing the two ends can silently disagree about. The engine still
	// reads "ipip" from a config that has it as GRE, so nothing already built
	// stops loading — but both ends have to be updated together, which the
	// handshake says plainly if they are not.
	const encap = "gre"
	greKey := uint32(0)

	cfg := l3Spec{Side: side, Carrier: carrier, Encap: encap, GREKey: greKey}
	// Chosen against what is already on the machine, so a second tunnel does
	// not land on the first one's subnet. See freeL3Subnet. On Iran they are
	// not asked at all — the code carries them to kharej — and can be changed
	// under the advanced settings.
	cfg.LocalIP, cfg.PeerIP = freeL3Subnet(side)

	// The Iran side dials out, which is the whole point of "direct". Its
	// questions are short and in the order an operator has the answers:
	// where, which port, what to forward, then the name and the token.
	if side == sideIran {
		host := tui.Prompt("Kharej IP Or Domain: ")
		if strings.TrimSpace(host) == "" {
			tui.Error("An address is required.")
			tui.PressEnter()
			return
		}
		port := tui.PromptDefault("Tunnel Port", "9000")
		if !validPort(port) {
			tui.Error("Invalid port.")
			tui.PressEnter()
			return
		}
		cfg.Addr = net.JoinHostPort(strings.TrimSpace(host), port)

		// Ports over a layer-3 tunnel are optional: without them it is a
		// plain private network (TUN) and routes whatever it is given.
		if raw := tui.Prompt("Forwarded Ports (Blank For TUN): "); strings.TrimSpace(raw) != "" {
			cfg.Ports = parsePorts(raw)
			if err := validatePortSpecs(cfg.Ports); err != nil {
				tui.Error(err.Error())
				tui.PressEnter()
				return
			}
		}
	} else {
		port := tui.PromptDefault("Tunnel Port", "9000")
		if !validPort(port) {
			tui.Error("Invalid port.")
			tui.PressEnter()
			return
		}
		cfg.Addr = net.JoinHostPort("0.0.0.0", port)

		// Exactly as the Iran server printed them: this machine's comes first.
		cfg.LocalIP = tui.PromptDefault("This Server's Tunnel Address", cfg.LocalIP)
		cfg.PeerIP = tui.PromptDefault("The Iran Server's Tunnel Address", cfg.PeerIP)
		for l3.CheckTunnelEnds(cfg.LocalIP, cfg.PeerIP) != nil {
			tui.Error("The Iran server's address cannot be this machine's own (" +
				hostOnly(cfg.LocalIP) + "). It is the address the Iran server has on the tunnel.")
			cfg.PeerIP = tui.PromptDefault("The Iran Server's Tunnel Address", "")
		}
	}

	cfg.Name = uniqueName(tui.PromptDefault("Tunnel Name", cfg.defaultName()))

	if !askL3Token(&cfg) {
		return
	}

	if side == sideIran && len(cfg.Ports) > 0 {
		cfg.AcceptUDP = tui.Confirm("Carry UDP As Well As TCP On Those Ports", false)
		// A second kharej asking for the first one's ports means "serve them
		// from both", not "fail to bind". See l3share.go. Asked after UDP,
		// because a shared port takes this tunnel's answer to it.
		cfg.Ports = offerL3Sharing(cfg)
		if busy := busyForwardPorts(cfg.Ports, cfg.PeerIP); len(busy) > 0 {
			tui.Error("Already in use on this server: " + strings.Join(busy, ", ") +
				" — the web panel's own port is the usual one. Pick other ports.")
			tui.PressEnter()
			return
		}
	}

	if !askL3CarrierExtras(&cfg, side) {
		return
	}

	askL3FEC(&cfg, side)
	askL3Paths(&cfg, carrier, side)

	cfg.MTU, cfg.Iface = defaultL3MTU, freeL3Iface()
	chooseL3Preset(false).apply(&cfg)
	if tui.Confirm("Fine-Tune The Advanced Settings", false) {
		askL3Advanced(&cfg, side, side == sideIran)
	}

	// The Iran side shows the kharej's setup link before anything is written,
	// so the whole tunnel — and what to paste on the other server — is on one
	// screen when the operator says yes.
	link := ""
	if side == sideIran {
		link = pendingShareLink(cfg)
	}
	summariseL3(cfg, link)
	if why := kharejPortClash(cfg); why != "" {
		tui.Error(why)
		tui.PressEnter()
		return
	}
	if !tui.Confirm("Create This Tunnel", true) {
		return
	}
	writeAndStart(cfg.Name, cfg.render(), cfg.Side, cfg.Token, link != "")
}

// pendingShareLink is the setup link the tunnel will have once it is written,
// built from the config the wizard is about to write. Empty if it cannot be
// built, in which case the link is printed after the tunnel is created, from
// the file, as before.
func pendingShareLink(cfg l3Spec) string {
	var c config.Config
	if _, err := toml.Decode(cfg.render(), &c); err != nil {
		return ""
	}
	link, err := shareLinkOf(cfg.Name, "", c)
	if err != nil {
		return ""
	}
	return link
}

// askL3CarrierExtras asks the questions only some carriers have. It reports
// false when the setup cannot go on.
func askL3CarrierExtras(cfg *l3Spec, side directSide) bool {
	// The SNI carrier has one question: which name to announce. It matters
	// more than most defaults do — the whole technique is that the box in
	// front already lets that name through, and which names those are is a
	// property of the route rather than of this program.
	if cfg.Carrier == "sni" && (side == sideIran || cfg.SNIDomain == "") {
		fmt.Println()
		tui.Info("This carrier sends a TLS hello naming a domain, once, at the start")
		tui.Info("of the flow. A filter that decides by server name reads it and lets")
		tui.Info("the rest of the connection through.")
		tui.Warn("Pick a domain your own route already reaches — a large local site is")
		tui.Warn("the usual answer. If the tunnel does not improve, try another.")
		fmt.Println()
		cfg.SNIDomain = strings.ToLower(strings.TrimSpace(
			tui.PromptDefault("Domain to announce", snispoof.DefaultDomain)))
	}

	// The forged-source carrier has a screen of its own: what the packets
	// should look like, where the replies go, and which address to forge. It
	// used to hang off the reverse spoof transport, which is where all of that
	// explanation was written; the carrier is a direct one now, so the screen
	// came with it. See askSpoofCarrier.
	if cfg.Carrier == "spoof" {
		askSpoofCarrier(&cfg.Spoof, side == sideIran)
		// Whatever the operator chose above, the listening side cannot work out
		// where to answer: every packet it receives carries a forged source. The
		// wizard asks for it, and the engine refuses to start without it, so it
		// is worth not letting the setup finish without it either.
		if side == sideKharej && net.ParseIP(cfg.Spoof.SpoofPeerIP) == nil {
			fmt.Println()
			tui.Error("This side needs the Iran server's real IP — it cannot be learned")
			tui.Error("from the forged packets, and the tunnel will not start without it.")
			tui.PressEnter()
			return false
		}
		// The one piece of host setup the tunnel cannot do for itself: a strict
		// reverse-path filter drops every forged-source packet before the tunnel
		// sees it. The peer's real address tells us which interface receives, so
		// the offer names the right one.
		peerReal := cfg.Spoof.SpoofPeerIP
		if side == sideIran {
			if host, _, err := net.SplitHostPort(cfg.Addr); err == nil {
				peerReal = host
			}
		}
		OfferRelaxRPFilter(cfg.Spoof.SpoofInterface, peerReal)
	}
	return true
}

// setupL3FromLink builds the kharej side of a tunnel from the Iran server's
// setup link (sharelink.go). Everything the two ends must agree on comes from
// the link, mirrored by MirrorForPeer; what is this machine's own business —
// the interface, the name — is chosen here.
func setupL3FromLink(chosen string) {
	// The one line the Iran server printed under its summary.
	link, err := DecodeShareLink(tui.Prompt("Setup Link: "))
	if err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	if link.Kind != "direct" || !strings.EqualFold(link.From, "iran") {
		tui.Error("This link is for the " + link.PeerSide() + " side of a " + link.Kind +
			" tunnel, not for the kharej side of a direct one.")
		tui.PressEnter()
		return
	}
	if link.Tr != chosen {
		tui.Warn("This link is for a " + link.Tr + " tunnel, not " + chosen +
			" — setting up " + link.Tr + ", as the Iran server has it.")
	}

	form := MirrorForPeer(link)
	form.Name = uniqueName(tui.PromptDefault("Tunnel Name", form.Name))
	if link.Tr == "spoof" {
		// The one carrier whose far end needs something of its own that a link
		// cannot carry: where the Iran server really is, behind its forged
		// sources. MirrorForPeer fills it when the link has it.
		if net.ParseIP(form.SpoofPeerIP) == nil {
			form.SpoofPeerIP = strings.TrimSpace(tui.Prompt("The Iran Server's Real IP: "))
		}
	}

	row := func(label, value string) {
		fmt.Printf("%s%-16s:%s %s\n", tui.Gray, label, tui.Reset, tui.Color(tui.White, value))
	}
	fmt.Println()
	tui.Rule()
	tui.Title("Direct " + carrierLabel(link.Tr) + " (Kharej)")
	fmt.Println()
	row("Name", form.Name)
	row("Listens On", "port "+form.TunnelPort)
	row("Interface", form.LocalIP+" ↔ "+form.PeerIP+" (the Iran server)")
	row("Config File", app.ConfigPath(form.Name))
	tui.Rule()
	fmt.Println()
	if !tui.Confirm("Create This Tunnel", true) {
		return
	}
	service, active, err := applyPeerForm(form)
	if err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	fmt.Println()
	if active {
		tui.Success("Created and running: " + service)
	} else {
		tui.Warn("Created, but " + service + " is not running yet — check its log.")
	}
	tui.Info("It comes up as soon as the Iran server dials in.")
	tui.PressEnter()
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
//
// addresses asks the tunnel addresses here, which the Iran side of the
// link-based flow does: they are chosen there, from the blocks free on the
// Iran server, and the link carries them to kharej. The classic flow asks
// them in the main questions instead.
func askL3Advanced(cfg *l3Spec, side directSide, addresses bool) {
	if addresses {
		fmt.Println()
		tui.Info("The two ends of the private network. The defaults are a block no")
		tui.Info("other tunnel on this server uses; the code carries them to kharej.")
		cfg.LocalIP = tui.PromptDefault("This server's tunnel address", cfg.LocalIP)
		cfg.PeerIP = tui.PromptDefault("The kharej server's tunnel address", cfg.PeerIP)
		for l3.CheckTunnelEnds(cfg.LocalIP, cfg.PeerIP) != nil {
			tui.Error("The kharej server's address cannot be this server's own (" + hostOnly(cfg.LocalIP) + ").")
			cfg.PeerIP = tui.PromptDefault("The kharej server's tunnel address", "")
		}
	}

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

	askL3FECPair(cfg)

	// Only the forwarded ports can be capped: routed traffic goes through the
	// interface and never passes the forwarder, so there is nothing to count.
	if side == sideIran && len(cfg.Ports) > 0 {
		fmt.Println()
		tui.Info("Both caps are off by default, and cover the forwarded ports only.")
		cfg.MaxConnections = tui.PromptInt("Maximum simultaneous connections (0 = unlimited)", cfg.MaxConnections)
		cfg.BandwidthMbps = tui.PromptInt("Maximum bandwidth in Mbit/s (0 = unlimited)", cfg.BandwidthMbps)
	}
}

// askL3Carrier offers the datagram carriers, in the operator's order: xDi,
// PCK, UDP, Quic, then IP and SNI spoofing for a route that needs them.
//
// A layer-3 tunnel carries IP packets, which already belong to something that
// handles its own loss; stacking that on a retransmitting transport makes
// throughput collapse rather than degrade, so tcp, ws and kcp are not offered
// at all.
func askL3Carrier() (string, bool) {
	// Built from the same list the panel offers, so the two cannot drift: they
	// used to be two literals in two files, which is a difference nobody sees
	// until an operator is told about a carrier one of the screens does not have.
	carriers := DirectCarriers()
	opts := make([]tui.Option, 0, len(carriers))
	for _, c := range carriers {
		desc := c["desc"]
		if c["needsRoot"] != "" {
			desc += " · needs root"
		}
		opts = append(opts, tui.Option{Title: c["label"], Desc: desc})
	}
	i := tui.ChooseOpt("How Should The Packets Travel?", opts)
	if i < 0 || i >= len(carriers) {
		return "", false
	}
	return carriers[i]["value"], true
}

// askGREKey is the RFC 2890 key, which both GRE kinds offer and mean the same
// thing by: a number that separates tunnels sharing the same two endpoints.
func askGREKey() uint32 {
	for {
		key := tui.PromptInt("Tunnel key (0 for none)", 0)
		// Compared as int64, not int.
		//
		// The upper bound is 2^32-1, which does not fit an int on a 32-bit
		// build — so `key <= 4294967295` was a constant overflow and the whole
		// package refused to compile for 386 and every 32-bit ARM. On a 32-bit
		// machine the prompt cannot return a number that large anyway; widening
		// the comparison keeps the check honest on 64-bit without asking the
		// compiler for something impossible on 32.
		if k := int64(key); k >= 0 && k <= math.MaxUint32 {
			return uint32(k)
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

// summariseL3 is the one screen before "Create This Tunnel": short, one fact a
// line, and on Iran the kharej's setup link under it.
//
// The kharej set up by hand is not left out: the link carries its port, its
// addresses and the token, and the same values are under Manage tunnels → the
// tunnel for anyone who types them in.
func summariseL3(cfg l3Spec, link string) {
	row := func(label, value string) {
		fmt.Printf("%s%-16s:%s %s\n", tui.Gray, label, tui.Reset, tui.Color(tui.White, value))
	}
	fmt.Println()
	tui.Rule()
	tui.Title("Direct " + carrierLabel(cfg.Carrier))
	fmt.Println()
	row("Interface", cfg.Iface+"  "+cfg.LocalIP+" ↔ "+hostOnly(cfg.PeerIP))
	if cfg.Side == sideIran {
		row("Dials", cfg.Addr)
		ports := "none (TUN)"
		if len(cfg.Ports) > 0 {
			ports = strings.Join(cfg.Ports, ", ")
			if cfg.AcceptUDP {
				ports += "  (TCP + UDP)"
			}
		}
		row("Forwarded Ports", ports)
	} else {
		row("Listens On", cfg.Addr)
	}
	if cfg.FECData > 0 && cfg.FECParity > 0 {
		row("Error Correction", fmt.Sprintf("%d spare per %d", cfg.FECParity, cfg.FECData))
	}
	if cfg.Paths > 1 {
		row("Sockets", fmt.Sprintf("%d  (UDP ports %s open on kharej)", cfg.Paths, socketPorts(cfg)))
	}
	if cfg.GREKey != 0 {
		row("GRE Key", fmt.Sprint(cfg.GREKey))
	}
	row("Tuning", presetLabel(cfg.Preset))
	row("Config File", app.ConfigPath(cfg.Name))
	if link != "" {
		fmt.Println()
		tui.Info("Setup Link (Setup Kharej → Direct → The Same Carrier → Setup Link) :")
		fmt.Println(tui.Color(tui.Bold+tui.White, link))
	}
	tui.Rule()
	fmt.Println()
}

// carrierLabel is the name a carrier has in the menu — PCK, ICMP — so the
// summary says what the operator chose in the words they chose it in.
func carrierLabel(value string) string {
	for _, c := range DirectCarriers() {
		if c["value"] == value {
			return c["label"]
		}
	}
	return strings.ToUpper(value)
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

// ---------------------------------------------------------------- writing

// createAndStart puts the rendered config on disk and brings the service up. It
// deliberately mirrors what finishSetup does for a reverse tunnel, so a direct
// tunnel is managed, backed up and deleted by exactly the same machinery. It
// reports false, having already said why and waited for Enter, on a failure.
func createAndStart(name, body string) bool {
	tui.Info("Applying system network optimizations...")
	optimize.ApplyQuiet(ReservedPorts())

	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		tui.Error("Cannot create the config directory: " + err.Error())
		tui.PressEnter()
		return false
	}
	if err := app.WriteFileAtomic(app.ConfigPath(name), []byte(body), app.TunnelConfigMode); err != nil {
		tui.Error("Cannot write the config: " + err.Error())
		tui.PressEnter()
		return false
	}
	if err := writeUnit(name); err != nil {
		tui.Error("Cannot create the service: " + err.Error())
		tui.PressEnter()
		return false
	}
	if err := DaemonReload(); err != nil {
		tui.Error("systemd reload failed: " + err.Error())
		tui.PressEnter()
		return false
	}

	service := app.ServiceName(name)
	if err := StartService(service); err != nil {
		tui.Error("The tunnel was created but would not start: " + err.Error())
		tui.Warn("Check the log with:  journalctl -u " + service + " -n 50")
		tui.PressEnter()
		return false
	}

	fmt.Println()
	if IsActive(service) {
		tui.Success(fmt.Sprintf("Tunnel %q is up and running (%s).", name, service))
	} else {
		tui.Warn(fmt.Sprintf("Tunnel %q created but not active yet — check the log:", name))
		tui.Warn("  journalctl -u " + service + " -n 50")
	}
	return true
}

// writeAndStart creates the tunnel and says what to do on the other server:
// on Iran, paste the setup link.
func writeAndStart(name, body string, side directSide, token string, linkShown bool) {
	if !createAndStart(name, body) {
		return
	}

	// Repeated here, after the tunnel exists, because this is the moment the
	// operator turns to the other machine — and the token they need has been
	// off the screen since the middle of the wizard.
	//
	// A kernel GRE tunnel has no token at all, and printing an empty one under
	// "copy this exactly" would be worse than saying nothing.
	if side == sideIran {
		// The setup link is what the kharej side is built from: everything
		// the two ends must agree on, in one line, so none of it is typed
		// twice. The summary showed it already; it is printed here only when
		// it could not be built before the file existed.
		if linkShown {
			tui.Info("Paste the setup link above on the kharej server. It is shown again under")
			tui.Info("Manage Tunnels → this tunnel → Setup Link.")
		} else {
			fmt.Println()
			tui.Rule()
			if !printShareLink(name) && token != "" {
				remindOtherSide(side, token)
			}
		}
	} else {
		fmt.Println()
		tui.Info("This side is ready. The tunnel comes up as soon as the Iran server dials in.")
	}

	tui.PressEnter()
}

// defaultL3FEC is the scheme the one-question answer picks.
//
// It comes from RecommendFEC rather than being written again here: that
// function already decides how much parity a given loss deserves, it is tested,
// and the Link Test uses it to retune a tunnel later. An unmeasured path is its
// "not measurable" tier, which is the case the wizard is in — nothing has been
// probed yet. Two places choosing this independently is how they come to
// disagree.
//
// Measured, on a link dropping 20%: an application saw 3.5% loss with this on
// and 39% with it off, for about a third more traffic.
func defaultL3FEC() FECPlan { return RecommendFEC(PathQuality{}) }

// askL3FEC offers forward error correction as one question about the path,
// rather than two numbers about Reed-Solomon.
//
// It is in the main flow rather than behind Fine Tune because it is the answer
// to a symptom people arrive with — a route that drops packets, a game that
// stutters — and because it is paired: the two ends must be set the same, and a
// setting buried in an optional screen is one that gets set on one machine.
func askL3FEC(cfg *l3Spec, side directSide) {
	there := "kharej"
	if side == sideKharej {
		there = "Iran"
	}

	// Worth it on a route that drops packets; about a third more traffic, so
	// pure waste on a clean one — hence the default no. On Iran the setup link
	// carries the answer; a kharej set up by hand must give the same one.
	if side == sideKharej {
		tui.Warn("The " + there + " end must answer this the same way.")
	}
	if !tui.Confirm("Turn On Error Correction (FEC)", false) {
		return
	}
	plan := defaultL3FEC()
	cfg.FECData, cfg.FECParity = plan.Data, plan.Parity
}

// askL3FECPair is the exact-numbers version for the advanced screen, for a path
// somebody has measured. Zero for either turns it off.
func askL3FECPair(cfg *l3Spec) {
	fmt.Println()
	tui.Info("Error correction, as an exact pair: for every DATA packets, PARITY")
	tui.Info("spare ones, and any PARITY of the group may be lost without loss.")
	tui.Info("0 for either turns it off. Both ends must use the same pair.")
	cfg.FECData = tui.PromptInt("Data packets per group", cfg.FECData)
	cfg.FECParity = tui.PromptInt("Spare packets per group", cfg.FECParity)
	if cfg.FECData <= 0 || cfg.FECParity <= 0 {
		cfg.FECData, cfg.FECParity = 0, 0
		tui.Info("Error correction off.")
		return
	}
	if cfg.FECParity >= cfg.FECData {
		tui.Error("More spare packets than payload costs more than the loss it repairs.")
		tui.Warn("Falling back to the recommended pair.")
		plan := defaultL3FEC()
		cfg.FECData, cfg.FECParity = plan.Data, plan.Parity
	}
}

// askL3Paths offers to spread the tunnel over several sockets, which is only
// worth anything on the plain UDP carrier — the obfuscated ones already vary
// their source per packet, so a shaper counting flows sees many either way.
//
// Like the other paired settings this is one question in the main flow rather
// than a number behind Fine Tune: both ends must use the same count, and the
// extra ports have to be open on the machine that listens.
func askL3Paths(cfg *l3Spec, carrier string, side directSide) {
	if carrier != "udp" {
		return
	}
	there := "kharej"
	if side == sideKharej {
		there = "Iran"
	}

	// For a provider that limits each connection: several sockets are several
	// connections, and several limits. Measured against a link capped at
	// 8 Mbit per connection: one socket carried 5.8 Mbit/s, four carried 23.6.
	// It takes one port per socket, counting up from the tunnel port, all open
	// on kharej — which the summary says. On Iran the setup link carries the
	// count; a kharej set up by hand must give the same one.
	if side == sideKharej {
		tui.Warn("The " + there + " end must use the same number of sockets.")
	}
	if !tui.Confirm("Spread The Tunnel Over Several Sockets", false) {
		return
	}
	for {
		n := tui.PromptInt("How Many Sockets (2-8)", 4)
		if n >= 2 && n <= 8 {
			cfg.Paths = n
			return
		}
		tui.Error("Choose between 2 and 8.")
	}
}

// socketPorts is the range of kharej ports a multi-socket udp tunnel uses,
// for the summary: "9000-9003".
func socketPorts(cfg l3Spec) string {
	base, err := strconv.Atoi(addrPort(cfg.Addr))
	if err != nil || cfg.Paths < 2 {
		return addrPort(cfg.Addr)
	}
	return fmt.Sprintf("%d-%d", base, base+cfg.Paths-1)
}

// l3PeerWithPrefix is the peer's tunnel address carrying this end's prefix
// length, which is how the other machine has to enter it as its own.
func l3PeerWithPrefix(cfg l3Spec) string {
	peer := hostOnly(cfg.PeerIP)
	if _, prefix, ok := strings.Cut(cfg.LocalIP, "/"); ok {
		return peer + "/" + prefix
	}
	return peer
}
