// Machine-level screens: the auto-refresh schedule, kernel and socket tuning,
// and uninstall.

package menu

import (
	"fmt"
	"os"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/optimize"
	"github.com/backpack/backpack/internal/schedule"
	"github.com/backpack/backpack/internal/telegram"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/webui"
)

// autoRefreshMenu lives under Manage.
func autoRefreshMenu() {
	tui.Clear()
	tui.Title("Auto Refresh Schedule")
	fmt.Println()
	tui.Info(fmt.Sprintf("Now: %s", refreshLabel()))
	fmt.Println()
	hours := tui.PromptInt("Restart Every (Hours, 0 = Off)", schedule.AutoRefreshHours())
	if err := schedule.SetAutoRefresh(hours); err != nil {
		tui.Error("Failed: " + err.Error())
	} else if hours <= 0 {
		tui.Success("Auto Refresh Off.")
	} else {
		// What the crontab will actually do, which is not always what was
		// typed: cron cannot say "every 36 hours", so anything above a day is
		// rounded down to whole days. Reporting the number that was asked for
		// would be repeating it back rather than confirming it.
		eff := schedule.EffectiveHours(hours)
		if eff != hours {
			tui.Info(fmt.Sprintf("Above 24 Hours It Is Whole Days: %d → %d.", hours, eff))
		}
		tui.Success(fmt.Sprintf("All Tunnels Restart Every %d Hour(s).", eff))
	}
	tui.PressEnter()
}

// optimizeMenu is main-menu item 6.
func optimizeMenu() {
	tui.Clear()
	tui.Title("Optimize — BBR, Buffers, Limits")
	fmt.Println()
	if !tui.Confirm("Apply Network Optimizations", true) {
		return
	}
	fmt.Println()
	optimize.Apply(func(line string) { tui.Info("• " + line) }, manage.ReservedPorts())
	fmt.Println()
	tui.Warn("Reboot To Fully Apply File Limits.")
	tui.PressEnter()
}

// uninstallMenu is main-menu item 9.
func uninstallMenu() {
	tui.Clear()
	tui.Title("Uninstall Backpack")
	fmt.Println()
	tui.Warn("Removes Everything: Tunnels, Configs, The Binary And " + app.InstallDir + " (Backups Too).")
	if !tui.Confirm("Are You Sure", false) {
		return
	}

	// Capture the install path before we delete the config that records it.
	repo := manage.InstallPath()
	if repo == "" {
		repo = app.InstallDir
	}

	for _, t := range manage.List() {
		_ = manage.Delete(t.Name)
	}
	_ = webui.Disable()
	_ = manage.DisableMonitorService()
	_ = schedule.SetAutoRefresh(0)
	_ = telegram.Disable()
	os.RemoveAll(app.ConfigDir)
	if err := os.Remove(app.BinPath); err != nil {
		tui.Warn("Could Not Remove " + app.BinPath + " — Remove It By Hand.")
	}
	if repo != "" && repo != "/" && repo != os.Getenv("HOME") {
		if err := os.RemoveAll(repo); err != nil {
			tui.Warn("Could Not Remove " + repo + " — Remove It By Hand.")
		} else {
			tui.Info("Removed: " + repo)
		}
	}
	tui.Success("Uninstalled. Goodbye!")
	os.Exit(0)
}
