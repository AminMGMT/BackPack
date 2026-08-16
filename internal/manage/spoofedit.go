package manage

import (
	"fmt"
	"net"
	"strings"

	"github.com/backpack/backpack/internal/tui"
)

// The Edit screen for the IP-spoofing carrier. Everything the setup wizard asks
// — and the obfuscation knobs it does not — can be changed here afterwards,
// one at a time.
//
// One at a time is the point. The wizard walks the whole carrier because it is
// building it; somebody who comes back here is fixing one thing, usually the
// forged source, because the tester has just told them which address gets
// through. Making them re-answer six questions to change one is how the wrong
// answer gets typed into the other five.

// SetSpoof writes a whole spoof block onto a tunnel and restarts it. It goes
// through the same SpoofTune the panel posts, so the terminal and the browser
// validate a forged address identically.
func SetSpoof(name string, f SpoofTune) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	if s.Transport != "spoof" {
		return fmt.Errorf("%q is not on the IP-spoofing transport", name)
	}
	if err := f.apply(&s); err != nil {
		return err
	}
	return applySpec(s)
}

// editSpoof is the "IP Spoofing" entry on the Edit menu. It loops so that
// changing one setting lands the reader back on the summary rather than back at
// the top of the tunnel's menu.
func editSpoof(name string, _ TunnelSpec) {
	for {
		spec, err := LoadSpec(name)
		if err != nil {
			tui.Error("Cannot read tunnel config: " + err.Error())
			tui.PressEnter()
			return
		}
		if spec.Transport != "spoof" {
			return
		}
		cur := spoofOf(spec)

		tui.Clear()
		tui.Title("IP Spoofing — " + name)
		fmt.Println()
		tui.Info("Packet profile   : " + spoofProfileSummary(spec))
		tui.Info("Forged source    : " + spoofSourceSummary(spec))
		if spec.Role == "server" {
			tui.Info("Replies go to    : " + orNone(spec.SpoofPeerIP))
		}
		tui.Info("Egress interface : " + orAuto(spec.SpoofInterface))
		tui.Info("Relay mode       : " + spoofPipeSummary(spec))
		tui.Info("Fingerprint      : " + spoofEvasionSummary(spec))
		fmt.Println()
		// Said here rather than only when a save is refused: without it nothing
		// on this screen can be written, and being told that while trying to
		// change the interface reads as the interface being the problem.
		if spec.Role == "server" && spec.SpoofPeerIP == "" {
			tui.Error("This server has no address to send its replies to, so the tunnel")
			tui.Error("cannot carry anything. Fill in \"The other end's real IPv4\" first —")
			tui.Error("nothing else here can be saved until it is set.")
			fmt.Println()
		}
		tui.Warn("Both ends must agree on the profile. Whether a forged source gets")
		tui.Warn("through is a property of the path — Manage → IP Spoofing Tester")
		tui.Warn("is what proves it.")
		fmt.Println()

		// Built side by side, like the tunnel menu above it: one entry only
		// exists on the server, and a numbered switch would have to be
		// re-counted every time that changed.
		opts := []tui.Option{
			{Title: "Packet profile", Desc: "how the packets look on the wire — both ends must match"},
			{Title: "Per-direction profiles", Desc: "a different profile each way, for a path that filters one of them"},
			{Title: "Forged source IP(s)", Desc: "the address stamped on what this end sends"},
		}
		actions := []func(){
			func() { editSpoofProfile(name, cur) },
			func() { editSpoofDirections(name, cur) },
			func() { editSpoofSources(name, cur) },
		}
		if spec.Role == "server" {
			opts = append(opts, tui.Option{
				Title: "The other end's real IPv4",
				Desc:  "where this server sends its replies — the forged packets do not say",
			})
			actions = append(actions, func() { editSpoofPeer(name, cur) })
		}
		opts = append(opts,
			tui.Option{Title: "Egress interface", Desc: "which device the raw packets leave by"},
			tui.Option{Title: "Relay mode", Desc: "bare datagram relay (no KCP) to a local UDP target — e.g. a whole WireGuard VPN"},
			tui.Option{Title: "Fingerprint & evasion", Desc: "TTL, DSCP, port shuffle, padding, fake TLS and the sizing knobs"},
		)
		actions = append(actions,
			func() { editSpoofInterface(name, cur) },
			func() { editSpoofPipe(name, cur, spec.Role) },
			func() { editSpoofEvasion(name, cur) },
		)

		idx := tui.ChooseOpt("Choose:", opts)
		if idx < 0 || idx >= len(actions) {
			return
		}
		actions[idx]()
	}
}

// applySpoof writes the edited block and reports the outcome the way every
// other Edit screen does.
func applySpoof(name string, f SpoofTune, what string) {
	if err := SetSpoof(name, f); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success(what + " — the tunnel restarted on the new settings.")
	tui.PressEnter()
}

func editSpoofProfile(name string, f SpoofTune) {
	fmt.Println()
	tui.Title("Packet profile")
	fmt.Println()
	tui.Info("What the forged packets are dressed as. UDP is the plainest and")
	tui.Info("passes nearly everywhere; ICMP and TCP are for a path that filters")
	tui.Info("UDP specifically.")
	tui.Warn("The other end must be changed to match, or the tunnel stops carrying.")
	fmt.Println()
	tui.Info("Currently : " + orNone(f.Profile))
	fmt.Println()

	p := askSpoofProfile("Packet profile:")
	if p == f.Profile {
		tui.Info("Nothing changed.")
		tui.PressEnter()
		return
	}
	f.Profile = p
	applySpoof(name, f, "Profile set to "+p)
}

func editSpoofDirections(name string, f SpoofTune) {
	fmt.Println()
	tui.Title("Per-direction profiles")
	fmt.Println()
	tui.Info("A path can filter one direction differently from the other. Uplink is")
	tui.Info("kharej → Iran, downlink is Iran → kharej. Almost no path needs this,")
	tui.Info("and both ends have to be set the same way.")
	fmt.Println()
	tui.Info("Currently : " + spoofDirectionSummary(f))
	fmt.Println()

	if !tui.Confirm("Set the two directions separately", f.Uplink != "" || f.Downlink != "") {
		if f.Uplink == "" && f.Downlink == "" {
			tui.Info("Nothing changed.")
			tui.PressEnter()
			return
		}
		f.Uplink, f.Downlink = "", ""
		applySpoof(name, f, "Both directions back on the single profile")
		return
	}
	f.Uplink = askSpoofProfile("Uplink profile (kharej → Iran):")
	f.Downlink = askSpoofProfile("Downlink profile (Iran → kharej):")
	applySpoof(name, f, "Directions set to "+f.Uplink+" up, "+f.Downlink+" down")
}

func editSpoofSources(name string, f SpoofTune) {
	fmt.Println()
	tui.Title("Forged source IP(s)")
	fmt.Println()
	tui.Info("The address stamped on the packets this machine sends.")
	fmt.Println()
	tui.Warn("Empty forges nothing: the tunnel runs on this machine's real address")
	tui.Warn("and still works. Several addresses, comma separated, are rotated")
	tui.Warn("through one per session — that is what gets past a block that counts")
	tui.Warn("by address.")
	fmt.Println()
	tui.Error("Only an address the IP Spoofing Tester says arrives is worth putting")
	tui.Error("here. One that does not leaves a tunnel that connects and carries")
	tui.Error("nothing, which reads as every other fault there is.")
	fmt.Println()
	tui.Info("Currently : " + orNone(f.SrcIPs))
	fmt.Println()

	raw := strings.TrimSpace(tui.PromptDefault("Forged source IPv4 (empty = do not forge)", f.SrcIPs))
	if raw == f.SrcIPs {
		tui.Info("Nothing changed.")
		tui.PressEnter()
		return
	}
	f.SrcIPs = raw
	applySpoof(name, f, "Forged source set to "+orNone(raw))
}

func editSpoofPeer(name string, f SpoofTune) {
	fmt.Println()
	tui.Title("The other end's real IPv4")
	fmt.Println()
	tui.Info("The kharej machine forges its source, so its packets do not say where")
	tui.Info("they came from. This server has to be told, or it has nowhere to send")
	tui.Info("the answers.")
	tui.Warn("The REAL public address of that server — the one you SSH into it with.")
	fmt.Println()
	tui.Info("Currently : " + orNone(f.PeerIP))
	fmt.Println()

	for {
		raw := strings.TrimSpace(tui.PromptDefault("Real IPv4 of the kharej server", f.PeerIP))
		if raw == f.PeerIP {
			tui.Info("Nothing changed.")
			tui.PressEnter()
			return
		}
		if net.ParseIP(raw).To4() == nil {
			tui.Error("That is not an IPv4 address. It looks like 203.0.113.10")
			continue
		}
		f.PeerIP = raw
		applySpoof(name, f, "Replies will go to "+raw)
		return
	}
}

func editSpoofInterface(name string, f SpoofTune) {
	fmt.Println()
	tui.Title("Egress interface")
	fmt.Println()
	tui.Info("Which device the raw packets leave by. Empty lets the kernel route,")
	tui.Info("which is right unless this machine has more than one uplink and you")
	tui.Info("know it picks the wrong one.")
	if names := routableInterfaces(); len(names) > 0 {
		tui.Warn("Available: " + strings.Join(names, ", "))
	}
	fmt.Println()
	tui.Info("Currently : " + orAuto(f.Interface))
	fmt.Println()

	for {
		raw := strings.TrimSpace(tui.PromptDefault("Interface (empty = automatic)", f.Interface))
		if raw == f.Interface {
			tui.Info("Nothing changed.")
			tui.PressEnter()
			return
		}
		if raw != "" {
			if _, err := net.InterfaceByName(raw); err != nil {
				tui.Error(fmt.Sprintf("no such interface: %v", err))
				continue
			}
		}
		f.Interface = raw
		applySpoof(name, f, "Egress interface set to "+orAuto(raw))
		return
	}
}

func editSpoofPipe(name string, f SpoofTune, role string) {
	fmt.Println()
	tui.Title("Relay mode")
	fmt.Println()
	tui.Info("Normally this tunnel wraps a reliable KCP tunnel over the forged-source")
	tui.Info("channel. Relay mode strips KCP and runs a bare datagram relay to a local")
	tui.Info("UDP target instead, so you carry something that brings its own")
	tui.Info("reliability — a whole WireGuard VPN, or another tunnel. The forwarded")
	tui.Info("ports are ignored in this mode.")
	tui.Warn("Both ends have to be in the same mode.")
	fmt.Println()
	tui.Info("Currently : " + onOff(f.Pipe))
	fmt.Println()

	on := tui.Confirm("Enable relay mode", f.Pipe)
	if !on {
		if !f.Pipe {
			tui.Info("Nothing changed.")
			tui.PressEnter()
			return
		}
		f.Pipe = false
		applySpoof(name, f, "Relay mode off — the KCP tunnel over the forwarded ports is used again")
		return
	}

	fmt.Println()
	if role == "server" {
		tui.Info("Where the inner service listens on THIS server (e.g. the real")
		tui.Info("WireGuard) — datagrams coming out of the tunnel are handed to it there.")
	} else {
		tui.Info("Where the tunnel should listen on THIS machine — point the inner app's")
		tui.Info("endpoint (e.g. WireGuard's `endpoint`) at exactly this address.")
	}
	def := f.PipeAddr
	if def == "" {
		def = "127.0.0.1:51820"
	}
	f.Pipe = true
	f.PipeAddr = strings.TrimSpace(tui.PromptDefault("Local UDP endpoint", def))
	applySpoof(name, f, "Relay mode on at "+f.PipeAddr)
}

// editSpoofEvasion is the obfuscation block. It is one screen of questions
// rather than a menu of its own: these are set together, when somebody is
// working out what a particular path will let past, and none of them means much
// on its own.
func editSpoofEvasion(name string, f SpoofTune) {
	fmt.Println()
	tui.Title("Fingerprint & evasion")
	fmt.Println()
	tui.Info("These change what the packets look like to whatever inspects them.")
	tui.Warn("All of them are off by default and none is needed for a working")
	tui.Warn("tunnel. Each one costs something — bandwidth, CPU, or a shape that a")
	tui.Warn("different filter notices instead. Change one at a time and test.")
	fmt.Println()

	tui.Info("These two describe the OTHER end and must match what it actually sends:")
	f.PeerSrcIP = strings.TrimSpace(tui.PromptDefault("Expected forged source from the other end", f.PeerSrcIP))
	f.DstIP = strings.TrimSpace(tui.PromptDefault("Forged destination address (icmp/tcp only)", f.DstIP))

	fmt.Println()
	tui.Info("Sizing:")
	f.MTU = tui.PromptInt("Fragment sends above this many bytes (0 = never)", f.MTU)
	f.SockBuf = tui.PromptInt("Socket buffer in bytes (0 = system default)", f.SockBuf)

	fmt.Println()
	tui.Info("Shape. The ICMP one makes the exchange read as ping and its answer")
	tui.Info("rather than two streams of requests, and needs the same answer on both")
	tui.Info("ends; the rest are local to this machine.")
	f.ICMPReply = tui.Confirm("ICMP profile: this pair asks and answers", f.ICMPReply)
	f.TTLJitter = tui.Confirm("Vary the TTL from packet to packet", f.TTLJitter)
	f.RandomDSCP = tui.Confirm("Vary the DSCP field from packet to packet", f.RandomDSCP)
	f.FakeTLS = tui.Confirm("Prepend a fake TLS record header (TCP profile only)", f.FakeTLS)

	fmt.Println()
	f.ShufflePort = tui.Confirm("Randomise the source port of every packet", f.ShufflePort)
	if f.ShufflePort {
		tui.Warn("Leave both at 0 to use the whole ephemeral range.")
		f.PortMin = tui.PromptInt("Source-port range, low", f.PortMin)
		f.PortMax = tui.PromptInt("Source-port range, high", f.PortMax)
	}

	fmt.Println()
	f.Padding = tui.Confirm("Append random padding to each frame", f.Padding)
	if f.Padding {
		tui.Warn("Padding hides the real length of what is carried, and costs that")
		tui.Warn("much bandwidth.")
		f.PaddingMax = tui.PromptInt("Most padding bytes per frame", f.PaddingMax)
	}

	applySpoof(name, f, "Fingerprint settings written")
}

// ---- the one-line summaries the Edit screen is built from --------------------

func spoofProfileSummary(s TunnelSpec) string {
	if s.SpoofUplink == "" && s.SpoofDownlink == "" {
		return orNone(s.SpoofProfile)
	}
	up, down := s.SpoofUplink, s.SpoofDownlink
	if up == "" {
		up = s.SpoofProfile
	}
	if down == "" {
		down = s.SpoofProfile
	}
	return fmt.Sprintf("%s up, %s down", up, down)
}

func spoofSourceSummary(s TunnelSpec) string {
	switch {
	case len(s.SpoofSrcPool) > 1:
		return fmt.Sprintf("%s (%d addresses, one per session)",
			strings.Join(s.SpoofSrcPool, ", "), len(s.SpoofSrcPool))
	case s.SpoofSrcIP != "":
		return s.SpoofSrcIP
	default:
		return "none — this machine's real address"
	}
}

func spoofPipeSummary(s TunnelSpec) string {
	if !s.SpoofPipe {
		return "off (kcp) — the KCP tunnel over the forwarded ports is used"
	}
	addr := s.SpoofPipeAddr
	if addr == "" {
		addr = "127.0.0.1:51820"
	}
	return "on (relay), forwarding to " + addr
}

// spoofEvasionSummary names what is turned on rather than listing every knob:
// the screen is a summary, and "off" repeated eight times is not one.
func spoofEvasionSummary(s TunnelSpec) string {
	var on []string
	for _, k := range []struct {
		on   bool
		name string
	}{
		{s.SpoofTTLJitter, "TTL jitter"},
		{s.SpoofRandomDSCP, "DSCP"},
		{s.SpoofShufflePort, "port shuffle"},
		{s.SpoofPadding, "padding"},
		{s.SpoofFakeTLS, "fake TLS"},
		{s.SpoofICMPReply, "ICMP reply"},
		{s.SpoofMTU > 0, "fragmenting"},
	} {
		if k.on {
			on = append(on, k.name)
		}
	}
	if len(on) == 0 {
		return "nothing on — plain packets"
	}
	return strings.Join(on, ", ")
}

func spoofDirectionSummary(f SpoofTune) string {
	if f.Uplink == "" && f.Downlink == "" {
		return "one profile both ways (" + orNone(f.Profile) + ")"
	}
	return fmt.Sprintf("%s up, %s down", orDefault(f.Uplink, f.Profile), orDefault(f.Downlink, f.Profile))
}

func orNone(v string) string {
	if v == "" {
		return "none"
	}
	return v
}
func orAuto(v string) string {
	if v == "" {
		return "automatic"
	}
	return v
}
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
