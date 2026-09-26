// Updates: the release channel, an update from the network or from a file,
// the relay used when the release host is unreachable, and restore points.

package menu

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/telegram"
	"github.com/backpack/backpack/internal/tui"
)

// updateMenu offers a safe update and the restore points it creates.
func updateMenu() {
	for {
		tui.Clear()
		tui.Title("Update Backpack")
		tui.Warn("Current version: " + app.Version)
		tui.Warn("Release channel : " + manage.ChannelLabel())
		fmt.Println()

		idx := tui.ChooseOpt("Choose:", []tui.Option{
			{Title: "Check for updates", Desc: "install the latest release — safely, with automatic rollback"},
			{Title: "Install from a downloaded file", Desc: localUpdateDesc()},
			{Title: "Restore points", Desc: "go back to a previous version if something went wrong"},
			{Title: "Release channel", Desc: "stable releases only, or also test pre-releases"},
		})
		switch idx {
		case 0:
			runUpdate()
		case 1:
			runLocalUpdate()
		case 2:
			restorePointMenu()
		case 3:
			channelMenu()
		default:
			return
		}
	}
}

// localUpdateDesc says whether there is a file to install, on the menu line, so
// the answer is visible before the option is chosen.
func localUpdateDesc() string {
	if u, ok := manage.FindLocalUpdate(); ok {
		if u.Version != "" {
			return "found " + u.Version + " in " + filepath.Dir(u.Path)
		}
		return "found " + filepath.Base(u.Path) + " in " + filepath.Dir(u.Path)
	}
	return "put " + manage.LocalAssetName() + " in /root first"
}

// runLocalUpdate installs a release the operator downloaded themselves.
//
// This exists because the download is the step that fails on the networks this
// project is for. Everything after it is the ordinary update — the same
// snapshot, health check and automatic rollback — so what is different here is
// only where the file came from.
func runLocalUpdate() {
	tui.Clear()
	tui.Title("Install from a downloaded file")
	fmt.Println()

	u, ok := manage.FindLocalUpdate()
	if !ok {
		tui.Error("No " + manage.LocalAssetName() + " found.")
		fmt.Println()
		tui.Info("Download it from the releases page on any machine that can reach")
		tui.Info("GitHub, copy it to this server, and choose this again:")
		fmt.Println()
		fmt.Printf("  %sscp %s root@this-server:/root/%s\n\n", tui.Gray, manage.LocalAssetName(), tui.Reset)
		tui.Info("Looked in: " + strings.Join(manage.LocalUpdateSearchedIn(), ", "))
		tui.Info("The name has to be exactly that — it says which architecture the")
		tui.Info("binary inside is built for, and this server runs " + runtime.GOARCH + ".")
		tui.PressEnter()
		return
	}

	fmt.Printf("  %sFile%s     %s\n", tui.Gray, tui.Reset, u.Path)
	fmt.Printf("  %sSize%s     %.1f MB\n", tui.Gray, tui.Reset, float64(u.Size)/(1<<20))
	fmt.Printf("  %sAdded%s    %s\n", tui.Gray, tui.Reset, u.When.Format("2006-01-02 15:04"))
	if u.Version != "" {
		fmt.Printf("  %sVersion%s  %s%s%s  (this server runs %s)\n",
			tui.Gray, tui.Reset, tui.Bold+tui.White, u.Version, tui.Reset, app.Version)
	} else {
		fmt.Printf("  %sVersion%s  %sunknown — the binary inside did not answer%s\n",
			tui.Gray, tui.Reset, tui.Gray, tui.Reset)
	}
	if u.Checksums != "" {
		fmt.Printf("  %sChecksum%s %s\n", tui.Gray, tui.Reset, u.Checksums)
	} else {
		fmt.Printf("  %sChecksum%s %sno SHA256SUMS beside it — it will be installed unverified%s\n",
			tui.Gray, tui.Reset, tui.Gray, tui.Reset)
	}
	fmt.Println()

	// Said plainly rather than refused. Reinstalling the same version is a
	// reasonable thing to want — a binary that was corrupted, a rollback being
	// undone — and going backwards is sometimes the whole point.
	if u.Version != "" && u.Version == app.Version {
		tui.Warn("That is the version already running. Installing it again is fine.")
	}

	tui.Info("A restore point is taken first. If a tunnel does not come back, the")
	tui.Info("update rolls itself back on its own.")
	fmt.Println()
	if !tui.Confirm("Install it?", true) {
		return
	}

	fmt.Println()
	if err := manage.ApplyLocalUpdate(u, func(l string) { tui.Info("• " + l) }); err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Done.")
	tui.PressEnter()
	reopen()
}

// channelMenu picks between stable releases and pre-releases.
func channelMenu() {
	tui.Clear()
	tui.Title("Release channel")
	fmt.Println()
	tui.Info("Current: " + manage.ChannelLabel())
	fmt.Println()
	tui.Warn("Stable installs finished releases only. Beta also installs")
	tui.Warn("pre-releases, so you can try a new version on one server before")
	tui.Warn("it reaches everyone — useful for testing, riskier for a server")
	tui.Warn("people depend on.")
	fmt.Println()

	opts, values := manage.ChannelOptions()
	idx := tui.ChooseOpt("Choose a channel:", opts)
	if idx < 0 {
		return
	}
	if err := manage.SetChannel(values[idx]); err != nil {
		tui.Error("Could not save the channel: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Release channel set to " + manage.ChannelLabel() + ".")
	tui.PressEnter()
}

// runUpdate checks for and installs a newer release. A restore point is taken
// first and the update rolls itself back if the services do not come back up.
func runUpdate() {
	tui.Clear()
	tui.Title("Check for updates")
	fmt.Println()
	tui.Info("Checking GitHub releases (direct, then through the tunnel relay)...")

	available, summary, err := manage.CheckUpdate()
	if err != nil {
		// Nothing could be reached. On the machine this matters most for — an
		// Iran server with working tunnels and no route to GitHub — the way out
		// is running the whole time, so offer it rather than stopping here.
		if !offerRelay(err) {
			return
		}
		available, summary, err = manage.CheckUpdate()
		if err != nil {
			tui.Error(err.Error())
			tui.PressEnter()
			return
		}
	}
	if !available {
		tui.Success(summary)
		tui.PressEnter()
		return
	}

	tui.Warn(summary)
	fmt.Println()
	tui.Info("A restore point is saved first. If anything fails to come back up,")
	tui.Info("Backpack puts the previous version back automatically.")
	fmt.Println()
	if !tui.Confirm("Download and install the update now", true) {
		return
	}
	fmt.Println()
	err = manage.ApplyUpdate(func(l string) { tui.Info("• " + l) })
	// The check reads a few hundred bytes and the download tens of megabytes,
	// so a route that answered the first can still fail the second. The same
	// offer applies, and only when a tunnel has not already been chosen.
	if err != nil && !manage.RelayChosen() {
		fmt.Println()
		if offerRelay(err) {
			err = manage.ApplyUpdate(func(l string) { tui.Info("• " + l) })
		}
	}
	if err != nil {
		tui.Error("Update failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Backpack updated successfully.")
	tui.PressEnter()
	reopen()
}

// offerRelay asks whether to fetch the update through one of the tunnels, and
// arranges it if so. It reports whether to carry on.
//
// The choice is put to the operator rather than made for them because taking it
// costs something: a tunnel that does not already expose the relay port has to
// be restarted to gain it, which interrupts whatever it is carrying for a
// moment. Tunnels that need no restart are offered first and say so.
func offerRelay(reason error) bool {
	options := manage.RelayOptions()
	if len(options) == 0 {
		tui.Error(reason.Error())
		fmt.Println()
		tui.Warn("No tunnel is online either, so there is no way out from here.")
		tui.Info("Install offline instead: download the release on a machine that can")
		tui.Info("reach GitHub and copy it across — see the README.")
		tui.PressEnter()
		return false
	}

	tui.Error(reason.Error())
	fmt.Println()
	tui.Info("This server cannot reach GitHub directly. One of its tunnels can:")
	tui.Info("the far end fetches the release and passes it back.")
	fmt.Println()

	opts := make([]tui.Option, len(options))
	for i, o := range options {
		desc := "restarts this tunnel briefly to open the relay port"
		if o.Ready {
			desc = "already carries the relay port — costs nothing"
		}
		opts[i] = tui.Option{Title: o.Name, Desc: desc}
	}
	idx := tui.ChooseOpt("Fetch the update through which tunnel?", opts)
	if idx < 0 || idx >= len(options) {
		return false
	}

	chosen := options[idx]
	if !chosen.Ready {
		fmt.Println()
		tui.Warn("Opening the relay port restarts " + chosen.Name + ". Traffic on it stops")
		tui.Warn("for a moment and comes back on its own.")
		if !tui.Confirm("Go ahead", true) {
			return false
		}
	}

	manage.UseRelay(chosen.Name)
	fmt.Println()
	tui.Info("Fetching through " + chosen.Name + "...")
	return true
}

// restorePointMenu lists saved restore points and can roll back to one.
func restorePointMenu() {
	tui.Clear()
	tui.Title("Restore points")
	tui.Warn("Saved automatically before every update — binary plus all configs.")
	fmt.Println()

	points := manage.ListSnapshots()
	if len(points) == 0 {
		tui.Info("No restore points yet — one is created the first time you update.")
		tui.PressEnter()
		return
	}

	opts := make([]tui.Option, len(points))
	for i, p := range points {
		desc := fmt.Sprintf("version %s", p.Meta.Version)
		if n := len(p.Meta.Tunnels); n > 0 {
			desc += fmt.Sprintf(" · %d tunnel(s)", n)
		}
		opts[i] = tui.Option{Title: p.Meta.Stamp, Desc: desc}
	}
	idx := tui.ChooseOpt("Roll back to which restore point:", opts)
	if idx < 0 {
		return
	}

	chosen := points[idx]
	fmt.Println()
	tui.Warn("This puts back the binary and ALL configs from " + chosen.Meta.Stamp + ",")
	tui.Warn("then restarts the panel and every tunnel.")
	if !tui.Confirm("Roll back now", false) {
		return
	}
	fmt.Println()
	if err := manage.RollbackUpdate(chosen, func(l string) { tui.Info("• " + l) }); err != nil {
		tui.Error("Rollback failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Rolled back to " + chosen.Meta.Version + " successfully.")
	tui.PressEnter()
	reopen()
}

// reopen replaces this menu with the binary now installed.
//
// An update or a rollback swaps the file on disk, and this process is still
// the build it was started as: the menu came back showing the old version and
// the old screens until the operator quit and ran sudo backpack again. Every
// tunnel has already been restarted on the new binary by then; this puts the
// menu on it too.
func reopen() {
	tui.Info("Opening the new version...")
	if err := execSelf(); err != nil {
		tui.Warn("Could not reopen by itself (" + err.Error() + ") — exit and run sudo backpack again.")
		tui.PressEnter()
	}
}

// execSelf starts the installed binary in place of this process. A variable so
// a test can see it asked for without the test binary being replaced.
var execSelf = func() error {
	return syscall.Exec(app.BinPath, []string{app.BinPath}, os.Environ())
}

// diagnoseRelay walks the relay chain and reports the first broken hop.
func diagnoseRelay() {
	tui.Clear()
	tui.Title("Relay diagnosis")
	fmt.Println()
	tui.Warn("Checking each hop between this server and Telegram...")
	fmt.Println()

	steps := telegram.DiagnoseRelay()
	for _, s := range steps {
		mark := tui.Color(tui.Bold+tui.Red, "✗")
		if s.OK {
			mark = tui.Color(tui.Bold+tui.White, "✓")
		}
		fmt.Printf("  %s %s%-16s%s %s%s%s\n",
			mark, tui.Bold+tui.White, s.Name, tui.Reset, tui.Gray, s.Detail, tui.Reset)
		if s.Fix != "" {
			tui.Error("      → " + s.Fix)
		}
	}

	fmt.Println()
	if len(steps) > 0 && steps[len(steps)-1].OK {
		tui.Success("Every hop is working — the bot should be able to send.")
	} else {
		tui.Warn("The first ✗ above is where it breaks. Everything below it was not reached.")
	}
	tui.PressEnter()
}
