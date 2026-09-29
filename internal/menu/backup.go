// Backup and restore: the configuration archive, the fleet key, the offsite
// copy, and the drill that proves a restore works before one is needed.

package menu

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/node"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/webui"
)

// backupMenu creates or restores a full configuration backup (all tunnels, the
// web-panel password, Telegram settings, certificates and the auto-refresh
// schedule) as a single portable .tar.gz archive kept under app.BackupDir.
func backupMenu() {
	for {
		tui.Clear()
		tui.Title("Backup & Restore")
		fmt.Println()
		tui.Warn("Every Tunnel And Setting In One File — " + app.BackupDir)
		fmt.Println()

		// Options and actions as parallel slices rather than a switch on the
		// index. Two of these items are conditional, so a switch would have to
		// be renumbered whenever one is inserted — and a missing case compiles
		// perfectly and silently returns to the previous screen.
		opts := []tui.Option{
			{Title: "Create Backup", Desc: "into " + app.BackupDir},
			{Title: "Restore Backup", Desc: "from the folder or a path"},
			{Title: "Off-Site Copy", Desc: offsiteLabel()},
			{Title: "Test A Restore", Desc: "changes nothing"},
		}
		actions := []func(){
			createBackup,
			restoreBackup,
			configureOffsite,
			testRestore,
		}
		// Only offered where it means something: a machine with no managed
		// servers has no sealed password and nothing to keep.
		if node.HasSealedPasswords() {
			opts = append(opts,
				tui.Option{Title: "Show Fleet Key", Desc: "to restore managed servers elsewhere"},
				tui.Option{Title: "Restore Fleet Key", Desc: "from another machine"})
			actions = append(actions, showFleetKey, restoreFleetKey)
		}

		idx := tui.ChooseOpt("Backup & Restore", opts)
		if idx < 0 || idx >= len(actions) {
			return
		}
		actions[idx]()
	}
}

// The fleet key, and why it is here rather than in the panel.
//
// A managed server's root password is sealed with a key that is deliberately
// not in the backup archive — see internal/node/seal.go. That is the right
// design: a backup is a thing people move, and it used to carry the root
// password of every managed server in the clear to wherever it went.
//
// The consequence nobody had hit yet is what happens when the panel machine is
// the one that dies. The archive restores onto a new machine, the fleet list
// comes back, and none of the credentials do. The safety mechanism works
// exactly as designed and the outcome is a fleet you cannot reach.
//
// So the key can be taken out and put back deliberately, by somebody with a
// shell on the machine. Not through the panel and not in the archive: the whole
// protection is that the two travel separately, and a button that put them back
// together would be the protection removed with a nicer name.
func showFleetKey() {
	key, err := node.ExportSealKey()
	if err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	fmt.Println()
	tui.Warn("Decrypts Managed Servers' Passwords — Keep It Away From The Backup.")
	fmt.Println()
	fmt.Println("  " + tui.Color(tui.Bold+tui.White, key))
	fmt.Println()
	tui.PressEnter()
}

// restoreFleetKey puts a previously kept key back on a machine that has none.
func restoreFleetKey() {
	fmt.Println()
	key := tui.Prompt("Fleet Key: ")
	if strings.TrimSpace(key) == "" {
		return
	}
	if err := node.ImportSealKey(key); err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Fleet Key Restored.")
	tui.PressEnter()
}

// createBackup writes a timestamped archive to the backup folder.
func createBackup() {
	dir := tui.PromptDefault("Directory", app.BackupDir)
	path, err := manage.BackupToFile(dir)
	if err != nil {
		tui.Error("Backup failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Backup Created:")
	tui.Info("  " + path)
	fmt.Println()
	tui.Warn("Keep It Private — It Holds Tokens And The Panel Password.")
	tui.PressEnter()
}

// restoreBackup restores tunnels and settings from an archive picked from the
// backup folder (or a manually entered path).
func restoreBackup() {
	archives, _ := filepath.Glob(app.BackupDir + "/*.tar.gz")

	var path string
	if len(archives) > 0 {
		opts := make([]tui.Option, 0, len(archives)+1)
		for _, a := range archives {
			opts = append(opts, tui.Option{Title: filepath.Base(a), Desc: "in " + app.BackupDir})
		}
		opts = append(opts, tui.Option{Title: "Other Path", Desc: ""})
		fmt.Println()
		idx := tui.ChooseOpt("Restore", opts)
		switch {
		case idx < 0:
			return
		case idx < len(archives):
			path = archives[idx]
		default:
			path = tui.Prompt("Backup File (.tar.gz): ")
		}
	} else {
		tui.Warn("None In " + app.BackupDir + ".")
		path = tui.Prompt("Backup File (.tar.gz): ")
	}
	if path == "" {
		return
	}

	f, err := os.Open(path)
	if err != nil {
		tui.Error("Cannot open file: " + err.Error())
		tui.PressEnter()
		return
	}
	defer f.Close()

	tui.Warn("Overwrites Tunnels And Settings With The Backup.")
	if !tui.Confirm("Restore", false) {
		return
	}

	res, err := manage.Restore(f)
	if err != nil {
		tui.Error("Restore failed: " + err.Error())
		tui.PressEnter()
		return
	}

	// Bring the web panel back up (it may have a restored password now) — unless
	// the restored config says its operator had stopped it.
	if !webui.Load().Stopped {
		if _, err := webui.EnsureRunning(); err != nil {
			tui.Warn("Panel Could Not Start: " + err.Error())
		} else if res.WebUIConfig {
			// The restored config may carry a different port/password — restart
			// the already-running panel so it actually serves with them.
			_ = manage.RestartService(app.WebUIService)
		}
	}

	tui.Success(fmt.Sprintf("Restored %d File(s).", res.Files))
	if len(res.Tunnels) > 0 {
		tui.Info(fmt.Sprintf("Tunnels: %d Registered, %d Started, %d Failed.",
			len(res.Tunnels), res.Started, res.Failed))
	}
	if res.AutoRefreshHours > 0 {
		tui.Info(fmt.Sprintf("Auto Refresh: Every %d Hour(s).", res.AutoRefreshHours))
	}
	if res.WebUIConfig {
		tui.Info("Panel Password Restored.")
	}
	tui.PressEnter()
}

// offsiteLabel summarises where backups are sent, for the menu row.
func offsiteLabel() string {
	cmd := manage.OffsiteCommand()
	if cmd == "" {
		return "not set; they stay on this machine"
	}
	if len(cmd) > 40 {
		return cmd[:37] + "..."
	}
	return cmd
}

// configureOffsite sets the command that copies a backup somewhere else.
//
// A command rather than a provider list: the operators this is for already have
// something that works — rsync to a machine they own, rclone to whatever they
// use, scp to a laptop — and a command is the interface they already have. It
// is also the one that does not need a release to support a new destination.
func configureOffsite() {
	tui.Clear()
	tui.Title("Off-Site Copy")
	fmt.Println()
	fmt.Println()
	tui.Info("Now: " + offsiteLabel())
	fmt.Println()
	tui.Warn("{} = The Backup File, e.g.  rclone copy {} remote:backpack/   (Blank = Off)")
	fmt.Println()
	fmt.Println()

	cmd := strings.TrimSpace(tui.Prompt("Command: "))
	if err := manage.SetOffsiteCommand(cmd); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	if cmd == "" {
		tui.Success("Off-Site Copy Off.")
		tui.PressEnter()
		return
	}

	tui.Success("Saved.")
	fmt.Println()
	// Offered rather than assumed: it runs a command the operator just typed
	// against a real file, and doing that without asking is a surprise.
	if !tui.Confirm("Try It With The Newest Backup", true) {
		tui.PressEnter()
		return
	}
	newest, err := manage.NewestBackup()
	if err != nil {
		tui.Warn("No Backup Yet — Create One First.")
		tui.PressEnter()
		return
	}
	tui.Info("Sending " + filepath.Base(newest) + "...")
	if err := manage.SendOffsite(newest); err != nil {
		tui.Error("It did not work: " + err.Error())
	} else {
		tui.Success("It Arrived — Weekly Backups Go There Too.")
	}
	tui.PressEnter()
}

// testRestore proves a backup would restore, without restoring it.
//
// A recovery procedure that has never been run is the ordinary state of a
// disaster-recovery plan and the reason they fail: the first time anybody
// exercises it is the day it has to work, on a machine that is already gone,
// with whatever the backup turned out not to contain.
func testRestore() {
	tui.Clear()
	tui.Title("Test A Restore")
	fmt.Println()
	tui.Warn("Checks A Backup Like A Real Restore — Changes Nothing.")
	fmt.Println()

	path, err := manage.NewestBackup()
	if err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	tui.Info("Newest: " + filepath.Base(path))
	fmt.Println()
	if other := strings.TrimSpace(tui.Prompt("Other File (Blank = Newest): ")); other != "" {
		path = other
	}

	rep, err := manage.TestRestore(path)
	if err != nil {
		tui.Error(err.Error())
		fmt.Println()
		tui.PressEnter()
		return
	}

	fmt.Println()
	fmt.Print(rep.Summary())
	fmt.Println()
	if len(rep.Warnings) == 0 {
		tui.Success("Would Restore Cleanly.")
	} else {
		tui.Warn("Would Restore, With The Notes Above.")
	}
	tui.PressEnter()
}
