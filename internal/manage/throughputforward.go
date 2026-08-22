package manage

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/tunnel/portmap"
)

// Measuring a port forwarder.
//
// The layer-3 half of this next door has a private subnet to work in: both ends
// hold an address on the tunnel's own interface, so a sink can be put on one
// and bytes pushed at it from the other without touching anything else. A port
// forwarder has no such address. Every port it carries is already spoken for by
// a real service, and there is no spare one to borrow.
//
// So the measurement runs through a mapping the tunnel already carries. The
// side that exposes the ports connects to one of them on its own loopback; the
// side that holds the backends puts the sink where that mapping's target
// points. What travels is a real forwarded connection over the real transport,
// which is the thing being measured.
//
// # What this costs, and why it is asked rather than assumed
//
// The sink has to bind the port the real backend uses, so for as long as the
// measurement runs that backend cannot be listening. That is a real service
// interruption on somebody's server, and it is not a thing to do quietly: the
// receiver says so plainly and the operator chooses the mapping.
//
// A mapping whose target names a host of its own is a different machine
// entirely, and no sink on this one will ever be reached. Those are named as
// unusable rather than offered and left to fail.

// forwardBackendHost is what a mapping's target means when it names no host:
// the loopback of the machine holding the backend. It matches the tunnel
// engines' own default — see direct.DefaultBackendHost — and is repeated here
// rather than imported so this screen does not depend on a tunnel package.
const forwardBackendHost = "127.0.0.1"

// forwardMapping is one mapping this screen can measure through.
type forwardMapping struct {
	Spec       string // as written in the config
	ListenPort int    // where the port-holding side accepts
	TargetPort int    // where the backend side must put the sink
	Reason     string // why it cannot be used, empty when it can
}

// Usable reports whether a measurement can run through this mapping.
func (m forwardMapping) Usable() bool { return m.Reason == "" }

// Label is how the mapping reads in a menu.
func (m forwardMapping) Label() string {
	if !m.Usable() {
		return fmt.Sprintf("%s — %s", m.Spec, m.Reason)
	}
	return fmt.Sprintf("port %d → backend port %d", m.ListenPort, m.TargetPort)
}

// forwardMappings expands a tunnel's configured ports into the mappings this
// screen can offer, keeping the unusable ones so they can be shown with a
// reason instead of silently disappearing.
func forwardMappings(t Tunnel) ([]forwardMapping, error) {
	expanded, err := portmap.Expand(t.Ports, forwardBackendHost)
	if err != nil {
		return nil, err
	}

	out := make([]forwardMapping, 0, len(expanded))
	for _, m := range expanded {
		fm := forwardMapping{Spec: m.String()}

		if _, p, err := net.SplitHostPort(m.Listen); err == nil {
			fm.ListenPort, _ = strconv.Atoi(p)
		}

		switch {
		case len(m.Targets) == 0:
			fm.Reason = "no backend to send to"
		case len(m.Targets) > 1:
			// Which member a connection lands on is the mapping's decision, so
			// there is no single port to tell the other end to listen on.
			fm.Reason = "load-balanced across several backends"
		default:
			host, p, err := net.SplitHostPort(m.Targets[0])
			if err != nil {
				fm.Reason = "backend address cannot be read"
				break
			}
			fm.TargetPort, _ = strconv.Atoi(p)
			if host != forwardBackendHost && host != "localhost" && host != "::1" {
				fm.Reason = "backend is on " + host + ", another machine"
			}
		}
		if fm.ListenPort == 0 && fm.Usable() {
			fm.Reason = "listen port cannot be read"
		}
		out = append(out, fm)
	}
	return out, nil
}

// pickForwardMapping offers the usable mappings, and shows the rest with the
// reason they are not on offer.
func pickForwardMapping(t Tunnel) (forwardMapping, bool) {
	mappings, err := forwardMappings(t)
	if err != nil {
		tui.Error("This tunnel's port mappings cannot be read: " + err.Error())
		tui.PressEnter()
		return forwardMapping{}, false
	}

	var usable []forwardMapping
	var blocked []forwardMapping
	for _, m := range mappings {
		if m.Usable() {
			usable = append(usable, m)
		} else {
			blocked = append(blocked, m)
		}
	}

	if len(blocked) > 0 {
		fmt.Println()
		tui.Warn("Not measurable, and why:")
		for _, m := range blocked {
			fmt.Printf("  · %s\n", m.Label())
		}
	}

	if len(usable) == 0 {
		fmt.Println()
		tui.Error("None of this tunnel's mappings can carry a measurement.")
		tui.Info("A measurable mapping forwards one port to one backend on the far")
		tui.Info("end's own loopback, which is what an ordinary mapping does.")
		tui.PressEnter()
		return forwardMapping{}, false
	}

	if len(usable) == 1 {
		return usable[0], true
	}

	opts := make([]tui.Option, len(usable))
	for i, m := range usable {
		opts[i] = tui.Option{Title: m.Label(), Desc: m.Spec}
	}
	idx := tui.ChooseOpt("Which mapping should carry the measurement?", opts)
	if idx < 0 {
		return forwardMapping{}, false
	}
	return usable[idx], true
}

// wrongSideForForward explains that this half belongs on the other machine.
//
// Getting the two halves the wrong way round is the mistake this whole screen
// exists to prevent, so it is worth answering in terms of what to do rather
// than what went wrong.
func wrongSideForForward(t Tunnel, wantReceiver bool) {
	fmt.Println()
	if wantReceiver {
		tui.Warn("This server exposes " + t.Name + "'s ports — it is the sending side.")
		tui.Info("The sink belongs on the server that holds the real services.")
		tui.Info("Run the receiver there, then choose \"Send and measure\" here.")
	} else {
		tui.Warn("This server holds " + t.Name + "'s backends — it is the receiving side.")
		tui.Info("The measurement is sent from the server that exposes the ports.")
		tui.Info("Run \"Send and measure\" there, and the receiver here.")
	}
	tui.PressEnter()
}

// askBackendPort asks the backend side which port to sink on.
//
// It has to be asked rather than read, because a reverse tunnel's port list
// lives only in the [server] table — this side genuinely does not know the
// mappings. The sending side prints the number to bring across.
func askBackendPort(t Tunnel) (int, bool) {
	fmt.Println()
	tui.Info("The sending server names the backend port to listen on — it is shown")
	tui.Info("there when the mapping is chosen. For a mapping written as a bare")
	tui.Info("port number it is that same number.")
	fmt.Println()

	port := tui.PromptInt("Backend port to sink on", 0)
	if port <= 0 || port > 65535 {
		tui.Error("A port between 1 and 65535 is required.")
		tui.PressEnter()
		return 0, false
	}

	if inUse := portIsBound(port); inUse {
		fmt.Println()
		tui.Warn(fmt.Sprintf("Something is already listening on port %d — almost", port))
		tui.Warn("certainly the real backend this mapping forwards to.")
		tui.Info("The sink cannot share the port, so that service has to be stopped")
		tui.Info("for the length of the measurement.")
		fmt.Println()
		if !tui.Confirm("Stop here and go do that", true) {
			tui.Info("Nothing was changed.")
		}
		tui.PressEnter()
		return 0, false
	}
	return port, true
}

// portIsBound reports whether something already accepts on the loopback port.
func portIsBound(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort(forwardBackendHost, strconv.Itoa(port)))
	if err != nil {
		return true
	}
	ln.Close()
	return false
}

// isForwardKind reports whether a tunnel is measured through one of its
// forwarded ports rather than across a private subnet.
func isForwardKind(t Tunnel) bool {
	return !strings.HasPrefix(t.Transport, "l3/")
}
