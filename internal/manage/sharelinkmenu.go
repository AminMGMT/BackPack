package manage

import (
	"fmt"
	"strings"

	"github.com/backpack/backpack/internal/tui"
)

// The setup link, from the operator's side.
//
// Everything below this was already built: the codec, the mirror that turns one
// side's settings into the other's, the validation, and error messages written
// for somebody holding a pasted string — "copy it again, all of it", "paste the
// setup link from the other server". All of it was reachable from nothing. The
// panel encoded a link and decoded it again in the same process as a way to
// derive a peer's config, and that was its only caller.
//
// So there was nowhere to get a link and nowhere to paste one, which is the
// whole feature missing while every part of it existed.
//
// It matters more than a convenience. A tunnel has around thirty paired
// settings, and the failure a mismatch produces is the most expensive one this
// system has: the tunnel comes up, reports itself connected, and carries
// nothing. Reading two files side by side is how that mismatch happens. A link
// is how it stops.

// showShareLink prints a tunnel's setup link for the operator to carry to the
// other server.
func showShareLink(name string) {
	tui.Clear()
	tui.Title("Setup Link")
	printShareLink(name)
	tui.PressEnter()
}

// printShareLink prints the link and what to do with it, and reports whether
// there was one to print.
func printShareLink(name string) bool {
	link, err := ShareLinkFor(name, "")
	if err != nil {
		tui.Error("Could not build the link: " + err.Error())
		return false
	}
	parsed, derr := DecodeShareLink(link)
	if derr != nil {
		// A link this build made and cannot read is a bug in the codec, not in
		// the operator's tunnel, and saying so is more use than the raw error.
		tui.Error("This build produced a link it cannot read back: " + derr.Error())
		return false
	}

	fmt.Println()
	if parsed.Kind == "direct" && parsed.PeerSide() == "kharej" {
		tui.Warn("On the KHAREJ server: Setup Kharej → Direct → the same carrier →")
		tui.Warn("Setup Link, and paste this line.")
	} else {
		tui.Warn("Paste this into the OTHER server: sudo backpack → Setup from a link.")
	}
	tui.Warn("It carries everything the two ends have to agree on — the token, the")
	tui.Warn("transport, the port, and the tuning — so nothing has to be retyped.")
	fmt.Println()
	tui.Info("Meant for the " + parsed.PeerSide() + " side. Shown again any time under")
	tui.Info("Manage tunnels → this tunnel → Setup Link.")
	fmt.Println()
	fmt.Println(link)
	fmt.Println()
	tui.Warn("It contains this tunnel's token. Treat it as the secret it is: anyone")
	tui.Warn("holding it can connect to this tunnel.")
	return true
}

// setupFromLink builds this machine's end from a link made on the other one.
func setupFromLink() {
	tui.Clear()
	tui.Title("Set up from a link")
	tui.Warn("Paste the setup link from the other server. It was shown there under")
	tui.Warn("Manage tunnels → the tunnel → Setup Link.")
	fmt.Println()

	raw := strings.TrimSpace(tui.Prompt("Link: "))
	if raw == "" {
		return
	}

	link, err := DecodeShareLink(raw)
	if err != nil {
		// Every refusal from the decoder is written for a person and says which
		// of them it was, so it is shown as it is rather than wrapped.
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}

	form := MirrorForPeer(link)
	fmt.Println()
	tui.Info("This will build the " + form.Side + " end of a " + form.Kind + " tunnel.")
	tui.Info("Name       : " + form.Name)
	tui.Info("Tunnel port: " + form.TunnelPort)
	if form.Transport != "" {
		tui.Info("Transport  : " + transportLabel(form.Transport))
	}
	if form.ServerAddr != "" {
		tui.Info("Other end  : " + form.ServerAddr)
	}
	fmt.Println()

	// The forwarded ports are already decided, or deliberately absent.
	//
	// MirrorForPeer fills them for the side that exposes them and leaves them
	// empty for the side that does not — the kharej end of a reverse tunnel
	// dials in and has no ports of its own to publish. Asking for them here
	// would be asking a question the link has already answered, and asking it
	// of the end that has no business answering it.
	if form.Ports != "" {
		tui.Info("Ports      : " + form.Ports)
		fmt.Println()
	}

	if !tui.Confirm("Create this tunnel", true) {
		return
	}

	service, active, err := applyPeerForm(form)
	if err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	if active {
		tui.Success("Created and running: " + service)
	} else {
		tui.Warn("Created, but " + service + " is not running yet — check its log.")
	}
	tui.PressEnter()
}

// applyPeerForm creates whichever kind of tunnel the form describes.
func applyPeerForm(f PeerForm) (service string, active bool, err error) {
	if f.Kind == "direct" {
		d := f.ToNewDirectTunnel()
		return CreateDirectTunnel(d)
	}
	t := f.ToNewTunnel()
	return CreateTunnel(t)
}

// SetupFromLink is the menu's entry point. Exported because internal/menu owns
// the main menu and this package owns everything it dispatches to.
func SetupFromLink() { setupFromLink() }
