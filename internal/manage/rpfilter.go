package manage

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/utils/network"
)

// rpFilterSysctlFile persists the relaxation so it survives a reboot, alongside
// the file Optimize writes rather than inside it: this is a spoof-only change an
// operator opted into for one tunnel, not part of the general tuning, and a
// separate file is one they can read and delete on its own.
const rpFilterSysctlFile = "/etc/sysctl.d/99-backpack-spoof.conf"

// OfferRelaxRPFilter checks whether reverse-path filtering will drop this
// machine's forged-source packets and, if it will, offers to relax it — the one
// piece of host setup a spoof tunnel needs that the tunnel cannot do for itself.
//
// It is offered rather than done silently because it changes a kernel security
// setting: rp_filter is what stops a machine accepting packets with impossible
// source addresses, and loosening it is a real decision, even though a spoof
// tunnel cannot receive without it. The operator sees exactly what will change
// and says yes.
//
// It sets 2 (loose), not 0 (off): loose still drops a source reachable via no
// interface at all, so it keeps most of the protection while letting a forged
// source that the default route covers through — which on a server with a
// default route is every useful one. If the tester later shows a source still
// not passing, 0 is the fallback, and the message says so.
//
// peerReal is the peer's real address, used to find the receiving interface
// when none was pinned; iface, if set, is spoof_interface.
func OfferRelaxRPFilter(iface, peerReal string) {
	if runtime.GOOS != "linux" {
		return
	}
	if iface == "" {
		iface = network.InterfaceTowardPeer(peerReal)
	}
	v, key := network.EffectiveRPFilter(iface)
	if v != 1 {
		return // already relaxed, or unreadable — nothing to offer
	}

	fmt.Println()
	tui.Warn("Reverse-path filtering is strict on this host (" + key + "=1).")
	tui.Warn("The kernel will DROP the forged-source packets before the tunnel")
	tui.Warn("sees them, so the tunnel will come up and carry nothing.")
	fmt.Println()
	tui.Info("Relaxing it to 2 (loose) lets the forged sources through while still")
	tui.Info("dropping packets from an address reachable nowhere at all.")
	if !tui.Confirm("Relax reverse-path filtering now", true) {
		tui.Info("Left unchanged. Set it yourself before relying on the tunnel:")
		tui.Info("  sysctl -w net.ipv4.conf.all.rp_filter=2")
		if iface != "" {
			tui.Info("  sysctl -w net.ipv4.conf." + iface + ".rp_filter=2")
		}
		return
	}

	keys := []string{"net.ipv4.conf.all.rp_filter"}
	if iface != "" {
		keys = append(keys, "net.ipv4.conf."+iface+".rp_filter")
	}
	if err := relaxRPFilter(keys); err != nil {
		tui.Error("Could not change it: " + err.Error())
		tui.Warn("Set it by hand: sysctl -w net.ipv4.conf.all.rp_filter=2")
		return
	}
	tui.Success("Reverse-path filtering relaxed (and saved to " + rpFilterSysctlFile + ").")
	tui.Info("If a forged source still does not pass the tester, set these to 0 instead.")
}

// relaxRPFilter writes the keys to a sysctl.d file so they survive a reboot, and
// applies them live. A failure to persist is not fatal — the live change still
// takes effect for this boot — but a failure to apply live is returned, because
// then nothing changed.
func relaxRPFilter(keys []string) error {
	var b strings.Builder
	b.WriteString("# Managed by backpack — reverse-path filtering for the spoof carrier\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s = 2\n", k)
	}
	// Best effort: keep going to the live apply even if the file cannot be written.
	_ = os.WriteFile(rpFilterSysctlFile, []byte(b.String()), 0644)

	applied := 0
	for _, k := range keys {
		if err := exec.Command("sysctl", "-w", k+"=2").Run(); err == nil {
			applied++
		}
	}
	if applied == 0 {
		return fmt.Errorf("no rp_filter key could be set (is this host running as root?)")
	}
	return nil
}
