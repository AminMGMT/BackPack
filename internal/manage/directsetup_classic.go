package manage

import (
	"fmt"
	"net"
	"strings"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/snispoof"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/tunnel/l3"
)

// The classic direct wizard, kept for IP spoofing and SNI spoofing.
//
// The other carriers are set up from the Iran server and a setup link. These
// two keep the wizard they had in v1.8.3, at the operator's request: both ends
// are set up by hand, the kharej side makes the token and the Iran side pastes
// it, and every screen keeps its explanation. Their answers — which forged
// source a route carries, which domain it lets through — belong to the route,
// and are worked out on each machine rather than handed over in one line.
//
// Two fixes made since then still apply here, because they are fixes rather
// than a change of flow: a tunnel address equal to the peer's is refused, and a
// forwarded port something already listens on is refused.

// setupL3Classic is setupL3 as it was, from after the carrier question.
func setupL3Classic(side directSide, carrier string) {
	const encap = "gre"
	cfg := l3Spec{Side: side, Carrier: carrier, Encap: encap}
	// Chosen against what is already on the machine, so a second tunnel does
	// not land on the first one's subnet. See freeL3Subnet.
	cfg.LocalIP, cfg.PeerIP = freeL3Subnet(side)

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
	} else {
		port := tui.PromptDefault("Tunnel port to listen on", "9000")
		if !validPort(port) {
			tui.Error("Invalid port.")
			tui.PressEnter()
			return
		}
		cfg.Addr = net.JoinHostPort("0.0.0.0", port)
	}

	fmt.Println()
	tui.Info("Private addresses for the two ends of the tunnel. The defaults are")
	tui.Info("fine unless " + l3Block(cfg.LocalIP) + "x is already used on either machine.")
	tui.Info("Both servers must agree, with the addresses swapped.")
	cfg.LocalIP = tui.PromptDefault("This machine's tunnel address", cfg.LocalIP)
	cfg.PeerIP = tui.PromptDefault("The other machine's tunnel address", cfg.PeerIP)
	for l3.CheckTunnelEnds(cfg.LocalIP, cfg.PeerIP) != nil {
		tui.Error("The other machine's address cannot be this machine's own (" + hostOnly(cfg.LocalIP) + ").")
		cfg.PeerIP = tui.PromptDefault("The other machine's tunnel address", "")
	}

	// Same order as the reverse wizard: address, name, token, ports,
	// carrier-specific extras, then optional tuning.
	cfg.Name = uniqueName(tui.PromptDefault("Tunnel name", cfg.defaultName()))

	token, ok := askSharedTokenClassic(side)
	if !ok {
		return
	}
	cfg.Token = token

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
			// A second kharej asking for the first one's ports means "serve
			// them from both", not "fail to bind". See l3share.go.
			cfg.Ports = offerL3Sharing(cfg)
			if busy := busyForwardPorts(cfg.Ports, cfg.PeerIP); len(busy) > 0 {
				tui.Error("Already in use on this server: " + strings.Join(busy, ", ") +
					" — the web panel's own port is the usual one. Pick other ports.")
				tui.PressEnter()
				return
			}
		}
	}

	// The SNI carrier has one question: which name to announce. It matters
	// more than most defaults do — the whole technique is that the box in
	// front already lets that name through, and which names those are is a
	// property of the route rather than of this program.
	if carrier == "sni" {
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

	// The forged-source carrier has a screen of its own; askL3CarrierExtras
	// holds it, with the checks that go with it.
	if carrier == "spoof" && !askL3CarrierExtras(&cfg, side) {
		return
	}

	askL3FECClassic(&cfg, side)

	cfg.MTU, cfg.Iface = defaultL3MTU, freeL3Iface()
	chooseL3Preset(true).apply(&cfg)
	if tui.Confirm("Fine-tune the advanced settings by hand", false) {
		askL3Advanced(&cfg, side, false)
	}

	summariseL3Classic(cfg)
	if !tui.Confirm("Create this tunnel", true) {
		return
	}
	if !createAndStart(cfg.Name, cfg.render()) {
		return
	}
	// Repeated after the tunnel exists, because this is the moment the
	// operator turns to the other machine — and the token has been off the
	// screen since the middle of the wizard.
	fmt.Println()
	tui.Rule()
	remindOtherSide(side, cfg.Token)
	tui.PressEnter()
}

// askSharedTokenClassic is the token question as it was: only the listening
// side — kharej — offers one, and the Iran side is asked for it, with no
// default to accept by reflex. Leaving it blank there generates one to carry
// over instead, so either order works without two different tokens.
func askSharedTokenClassic(side directSide) (string, bool) {
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

// askL3FECClassic is the error-correction question with its explanation.
func askL3FECClassic(cfg *l3Spec, side directSide) {
	there := "kharej"
	if side == sideKharej {
		there = "Iran"
	}

	fmt.Println()
	tui.Info("Does this route drop packets? A congested international path or a")
	tui.Info("lossy last mile shows up as a game that stutters, calls that break")
	tui.Info("up, or transfers that crawl while the link looks idle.")
	fmt.Println()
	tui.Info("Error correction sends a few spare packets with every group, so the")
	tui.Info("far end rebuilds what the path lost instead of waiting for it again.")
	tui.Warn("It costs about a third more traffic. On a clean route that is pure")
	tui.Warn("waste — say no unless you have a reason.")
	tui.Warn("The " + there + " end must answer this the same way.")
	fmt.Println()
	if !tui.Confirm("Turn on error correction", false) {
		return
	}
	plan := defaultL3FEC()
	cfg.FECData, cfg.FECParity = plan.Data, plan.Parity
	tui.Success(fmt.Sprintf("Error correction on: %d spare packets per %d.", cfg.FECParity, cfg.FECData))
}

// summariseL3Classic is the summary as it was: everything, then the values the
// other machine must match, then the token.
func summariseL3Classic(cfg l3Spec) {
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
	// Spelled out because these are the values that must be identical on the
	// other machine, and getting one wrong produces a tunnel that comes up,
	// reports a peer and carries nothing.
	tui.Warn("The other machine must use exactly these three:")
	tui.Warn("  carrier " + cfg.Carrier + "   wrapping " + encap + "   the same token")
	// And the addresses, the other way round.
	if cfg.Side == sideIran {
		tui.Warn("  and these tunnel addresses when its wizard asks:")
		tui.Warn("    this machine's tunnel address   " + l3PeerWithPrefix(cfg))
		tui.Warn("    the other machine's address     " + hostOnly(cfg.LocalIP))
	}
	fmt.Println()
	tui.Warn("Once both ends are up, test it with:  ping " + hostOnly(cfg.PeerIP))
	fmt.Println()
	remindOtherSide(cfg.Side, cfg.Token)
}

func sideLabel(s directSide) string {
	if s == sideIran {
		return "Iran (dials out, exposes the ports)"
	}
	return "Kharej (listens, holds the service)"
}
