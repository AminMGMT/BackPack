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
	host := linkHost()
	link, err := shareLinkFor(name, host)
	if err != nil {
		tui.Error("Could not build the link: " + err.Error())
		return false
	}
	parsed, derr := DecodeShareLink(link)
	if derr != nil {
		// A link this build made and cannot read is a bug in the codec, not in
		// the operator's tunnel, and saying so is more use than the raw error.
		tui.Error("The link does not read back: " + derr.Error())
		return false
	}

	fmt.Println()
	if parsed.Kind == "direct" && parsed.PeerSide() == "kharej" {
		tui.Warn("On The Kharej: Setup Kharej → Direct → Same Carrier → Setup Link.")
	} else {
		tui.Warn("On The Other Server: sudo backpack → Manage → Set Up From A Link.")
	}
	fmt.Println()
	tui.Info("For The " + titleWord(parsed.PeerSide()) + " Side.")
	if host == "" && parsed.PeerNeedsAddress() {
		tui.Warn("No Public Address Found — The Other Side Will Ask For It.")
	}
	if parsed.PeerSide() == "kharej" {
		printLinkBlock(link, "sudo backpack → Setup Kharej → Setup Link")
	} else {
		fmt.Println()
		fmt.Println(link)
	}
	fmt.Println()
	tui.Warn("It Holds The Token — Keep It Private.")
	return true
}

// setupFromLink builds this machine's end from a link made on the other one.
func setupFromLink() {
	tui.Clear()
	tui.Title("Set Up From A Link")
	fmt.Println()

	raw := strings.TrimSpace(tui.Prompt("Setup Link: "))
	if raw == "" {
		return
	}

	link, err := DecodeShareLink(FindSetupLink(raw))
	if err != nil {
		// Every refusal from the decoder is written for a person and says which
		// of them it was, so it is shown as it is rather than wrapped.
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}

	form := MirrorForPeer(link)
	if !askMissingAddress(&form) {
		return
	}
	fmt.Println()
	tui.Info("Builds The " + titleWord(form.Side) + " End Of A " + titleWord(form.Kind) + " Tunnel.")
	tui.Info("Name       : " + form.Name)
	tui.Info("Tunnel Port: " + form.TunnelPort)
	if form.Transport != "" {
		tui.Info("Transport  : " + transportLabel(form.Transport))
	}
	if form.ServerAddr != "" {
		tui.Info("Other End  : " + form.ServerAddr)
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

	if !tui.Confirm("Create This Tunnel", true) {
		return
	}

	service, active, err := applyPeerForm(form)
	if err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	if active {
		tui.Success("Created And Running: " + service)
	} else {
		tui.Warn("Created, But " + service + " Is Not Running — Check Its Log.")
	}
	scheduleFromLink(form.Name, link)
	tui.PressEnter()
}

// applyPeerForm creates whichever kind of tunnel the form describes.
func applyPeerForm(f PeerForm) (service string, active bool, err error) {
	if f.ManagedLink != nil {
		raw, err := f.ManagedLink.Encode()
		if err != nil {
			return "", false, err
		}
		applied, err := ApplySetupLink(raw, LinkApplyOptions{Name: f.Name, Host: f.ServerAddr})
		return applied.Service, applied.Active, err
	}
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

// linkHost is this server's address as the other end reaches it, for a link
// made from Manage. The wizards ask for it and put it in their link; this link
// was built with none, so a kharej given it through Setup from a link — which
// had no way to ask — was refused with "the server address is required",
// while the same tunnel typed in by hand came up. Reported on v1.8.4 as "the
// setup link did not work for a reverse tunnel, doing it manually did". Empty
// when the address is not known; the other side then asks for it.
var linkHost = func() string {
	if ip := PublicIPv4(); ip != "-" {
		return ip
	}
	return ""
}

// shareLinkFor is ShareLinkFor; a variable so a test can hand it a tunnel that
// is not on disk.
var shareLinkFor = ShareLinkFor

// askMissingAddress asks for the other server's address when the link did not
// carry one and the side being built has to dial it. It reports false when
// there is still none, after saying so.
func askMissingAddress(f *PeerForm) bool {
	if !f.NeedsServerAddr() || strings.TrimSpace(f.ServerAddr) != "" {
		return true
	}
	label := "Iran IP Or Domain: "
	if f.Kind == "direct" {
		label = "Kharej IP Or Domain: "
	}
	tui.Warn("The Link Has No Address For The Other Server.")
	f.ServerAddr = strings.Trim(strings.TrimSpace(tui.Prompt(label)), "[]")
	if f.ServerAddr == "" {
		tui.Error("An address is required.")
		tui.PressEnter()
		return false
	}
	return true
}
