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
		tui.Warn("A backup bundles every tunnel, the web-panel password, Telegram")
		tui.Warn("settings, TLS certs and the auto-refresh schedule into one file.")
		tui.Warn("Backups live in " + app.BackupDir)
		fmt.Println()

		// Options and actions as parallel slices rather than a switch on the
		// index. Two of these items are conditional, so a switch would have to
		// be renumbered whenever one is inserted — and a missing case compiles
		// perfectly and silently returns to the previous screen.
		opts := []tui.Option{
			{Title: "Create a backup file", Desc: "saved into " + app.BackupDir},
			{Title: "Restore from a backup file", Desc: "pick one from the folder or enter a path"},
			{Title: "Copy backups off this machine", Desc: "where the weekly backup is sent — " + offsiteLabel()},
			{Title: "Test a restore", Desc: "prove a backup file would actually restore, changing nothing"},
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
				tui.Option{Title: "Show the fleet key", Desc: "needed to restore managed servers onto a DIFFERENT machine"},
				tui.Option{Title: "Restore the fleet key", Desc: "paste a key kept from another machine"})
			actions = append(actions, showFleetKey, restoreFleetKey)
		}

		idx := tui.ChooseOpt("Choose:", opts)
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
	tui.Warn("This key decrypts the stored passwords of every managed server.")
	tui.Warn("Keep it somewhere the BACKUP IS NOT. Storing them together undoes")
	tui.Warn("the only thing sealing them achieves.")
	tui.Warn("You need it only to restore this fleet onto a different machine.")
	fmt.Println()
	fmt.Println("  " + tui.Color(tui.Bold+tui.White, key))
	fmt.Println()
	tui.PressEnter()
}

// restoreFleetKey puts a previously kept key back on a machine that has none.
func restoreFleetKey() {
	fmt.Println()
	tui.Info("Paste the fleet key from the machine this backup came from.")
	key := tui.Prompt("Fleet key: ")
	if strings.TrimSpace(key) == "" {
		return
	}
	if err := node.ImportSealKey(key); err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Fleet key restored. The managed servers' passwords are readable again.")
	tui.PressEnter()
}

// createBackup writes a timestamped archive to the backup folder.
func createBackup() {
	dir := tui.PromptDefault("Save the backup in which directory", app.BackupDir)
	path, err := manage.BackupToFile(dir)
	if err != nil {
		tui.Error("Backup failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Backup created:")
	tui.Info("  " + path)
	fmt.Println()
	tui.Warn("Keep it private — it contains tokens and the panel password.")
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
		opts = append(opts, tui.Option{Title: "Enter a custom path", Desc: "an archive somewhere else"})
		fmt.Println()
		idx := tui.ChooseOpt("Restore which backup:", opts)
		switch {
		case idx < 0:
			return
		case idx < len(archives):
			path = archives[idx]
		default:
			path = tui.Prompt("Path to the backup .tar.gz file: ")
		}
	} else {
		tui.Warn("No backups found in " + app.BackupDir + " — enter a path manually.")
		path = tui.Prompt("Path to the backup .tar.gz file: ")
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

	tui.Warn("This overwrites existing tunnels/settings with the backup's contents.")
	if !tui.Confirm("Restore now", false) {
		return
	}

	res, err := manage.Restore(f)
	if err != nil {
		tui.Error("Restore failed: " + err.Error())
		tui.PressEnter()
		return
	}

	// Bring the web panel back up (it may have a restored password now).
	if _, err := webui.EnsureRunning(); err != nil {
		tui.Warn("Web panel could not start: " + err.Error())
	} else if res.WebUIConfig {
		// The restored config may carry a different port/password — restart the
		// already-running panel so it actually serves with them.
		_ = manage.RestartService(app.WebUIService)
	}

	tui.Success(fmt.Sprintf("Restored %d file(s).", res.Files))
	if len(res.Tunnels) > 0 {
		tui.Info(fmt.Sprintf("Tunnels: %d re-registered, %d started, %d failed.",
			len(res.Tunnels), res.Started, res.Failed))
	}
	if res.AutoRefreshHours > 0 {
		tui.Info(fmt.Sprintf("Auto-refresh restored: every %d hour(s).", res.AutoRefreshHours))
	}
	if res.WebUIConfig {
		tui.Info("Web-panel password restored from the backup.")
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
	tui.Title("Copy backups off this machine")
	fmt.Println()
	tui.Warn("Backups are written to " + app.BackupDir + " — on the server they")
	tui.Warn("describe. The case they exist for is the case where that server is")
	tui.Warn("gone, so a copy somewhere else is the only one that will be there.")
	fmt.Println()
	tui.Info("Now: " + offsiteLabel())
	fmt.Println()
	tui.Warn("Enter a command. {} is replaced with the backup file's path:")
	tui.Warn("    rclone copy {} remote:backpack/")
	tui.Warn("    scp {} backup@10.0.0.9:/srv/backpack/")
	tui.Warn("    restic backup {}")
	fmt.Println()
	tui.Warn("It is run directly, not through a shell, so a ';' or a '|' in it is")
	tui.Warn("a word rather than syntax. For a pipeline write: sh -c '...'")
	tui.Warn("Leave empty to stop copying them anywhere.")
	fmt.Println()

	cmd := strings.TrimSpace(tui.Prompt("Command: "))
	if err := manage.SetOffsiteCommand(cmd); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	if cmd == "" {
		tui.Success("Backups will stay on this machine.")
		tui.PressEnter()
		return
	}

	tui.Success("Saved.")
	fmt.Println()
	// Offered rather than assumed: it runs a command the operator just typed
	// against a real file, and doing that without asking is a surprise.
	if !tui.Confirm("Try it now with the newest backup", true) {
		tui.PressEnter()
		return
	}
	newest, err := manage.NewestBackup()
	if err != nil {
		tui.Warn("There is no backup to try it with yet — take one first.")
		tui.PressEnter()
		return
	}
	tui.Info("Sending " + filepath.Base(newest) + "...")
	if err := manage.SendOffsite(newest); err != nil {
		tui.Error("It did not work: " + err.Error())
		tui.Warn("Fix the command or the destination; the weekly backup would have")
		tui.Warn("failed the same way, and you would have found out much later.")
	} else {
		tui.Success("It arrived. The weekly backup will be copied there too.")
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
	tui.Title("Test a restore")
	fmt.Println()
	tui.Warn("This stages a backup exactly as a real restore would — the same")
	tui.Warn("checks, the same refusals — into a scratch directory, reports what")
	tui.Warn("it holds, and throws it away. Nothing on this machine changes.")
	fmt.Println()

	path, err := manage.NewestBackup()
	if err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	tui.Info("Newest backup: " + filepath.Base(path))
	fmt.Println()
	if other := strings.TrimSpace(tui.Prompt("Path to test (empty for the one above): ")); other != "" {
		path = other
	}

	rep, err := manage.TestRestore(path)
	if err != nil {
		tui.Error(err.Error())
		fmt.Println()
		tui.Warn("This is the answer you want *now* rather than during a recovery.")
		tui.PressEnter()
		return
	}

	fmt.Println()
	fmt.Print(rep.Summary())
	fmt.Println()
	if len(rep.Warnings) == 0 {
		tui.Success("This backup would restore cleanly.")
	} else {
		tui.Warn("It would restore, with the caveats above.")
	}
	tui.PressEnter()
}
