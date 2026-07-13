// Package menu implements the interactive backpack CLI shown when the binary
// is run without a config file.
package menu

import (
	"fmt"
	"os"
	"sync/atomic"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/optimize"
	"github.com/backpack/backpack/internal/schedule"
	"github.com/backpack/backpack/internal/telegram"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/webui"
)

// ipStore caches the server's public IPv4 so the frequently-redrawn header
// never blocks on a network lookup.
var ipStore atomic.Value // holds string

// Run starts the interactive menu loop.
func Run() {
	requireRoot()

	// Bring the web panel up immediately so it's ready before the first tunnel,
	// and start resolving the public IP in the background for the header.
	if _, err := webui.EnsureRunning(); err != nil {
		tui.Warn("Web panel could not start: " + err.Error())
		tui.PressEnter()
	}
	go resolveServerIP()

	for {
		tui.Clear()
		tui.Logo(app.Version)
		printHeader()
		printMenu()

		switch tui.Prompt("Select an option: ") {
		case "1":
			manage.SetupServer()
			afterSetupWeb()
		case "2":
			manage.SetupClient()
			afterSetupWeb()
		case "3":
			webPanelMenu()
		case "4":
			manageMenu()
		case "5":
			autoRefreshMenu()
		case "6":
			manage.StatusLive()
		case "7":
			optimizeMenu()
		case "8":
			telegramMenu()
		case "9":
			updateMenu()
		case "10":
			uninstallMenu()
		case "11", "0":
			tui.Info("Goodbye!")
			return
		default:
			tui.Error("Invalid option.")
			tui.PressEnter()
		}
	}
}

// printHeader shows the version, web-panel access details and a live summary
// of tunnels and the auto-refresh schedule under the logo.
func printHeader() {
	tui.Rule()

	cfg := webui.Load()
	if webui.Running() {
		url := fmt.Sprintf("http://%s:%d", cachedServerIP(), cfg.Port)
		headerLine("Web Panel", tui.Color(tui.Bold+tui.White, url)+"  "+tui.Color(tui.Green, "● running"))
		headerLine("Login code", tui.Color(tui.Bold+tui.Yellow, cfg.Password))
	} else {
		headerLine("Web Panel", tui.Color(tui.Red, "○ stopped")+tui.Color(tui.Gray, "  (start it from menu → Web Panel)"))
	}

	tunnels := manage.List()
	running := 0
	for _, t := range tunnels {
		if manage.IsActive(t.Service) {
			running++
		}
	}
	headerLine("Tunnels", fmt.Sprintf("%d total · %s%d running%s", len(tunnels), tui.Green, running, tui.Reset))
	headerLine("Auto-refresh", refreshLabel())

	tui.Rule()
}

// headerLine prints one aligned "Label   value" row in the status block.
func headerLine(label, value string) {
	fmt.Printf("  %s%-12s%s %s\n", tui.Cyan, label, tui.Reset, value)
}

// printMenu renders the main menu with a short description beside each option.
func printMenu() {
	fmt.Println()
	menuItem(1, "Setup Server", "Iran side — exposes ports to users")
	menuItem(2, "Setup Client", "Kharej side — dials out to the service")
	menuItem(3, "Web Panel", "link, login code, start / stop")
	menuItem(4, "Manage", "tunnels, status, backup & restore")
	menuItem(5, "Auto Refresh", "restart all tunnels every N hours")
	menuItem(6, "Status", "live tunnel table")
	menuItem(7, "Optimize", "kernel & network tuning (BBR, buffers)")
	menuItem(8, "Telegram Bot", "status reports to your admin")
	menuItem(9, "Update", "pull latest & rebuild")
	menuItem(10, "Uninstall", "remove everything")
	menuItem(11, "Exit", "")
	fmt.Println()
}

// menuItem prints one aligned, colored menu row.
func menuItem(n int, title, desc string) {
	num := tui.Color(tui.Magenta, fmt.Sprintf("%2d)", n))
	if desc == "" {
		fmt.Printf("  %s %s%-14s%s\n", num, tui.Bold, title, tui.Reset)
		return
	}
	fmt.Printf("  %s %s%-14s%s %s%s%s\n",
		num, tui.Bold, title, tui.Reset, tui.Gray, desc, tui.Reset)
}

// cachedServerIP returns the resolved public IPv4 if known, otherwise a
// placeholder — it never blocks, so it's safe for the redrawn header.
func cachedServerIP() string {
	if v, _ := ipStore.Load().(string); v != "" {
		return v
	}
	return "detecting…"
}

// resolveServerIP fetches and caches the public IPv4 (blocking). Used where an
// accurate value matters, e.g. when showing the panel credentials.
func resolveServerIP() string {
	if v, _ := ipStore.Load().(string); v != "" {
		return v
	}
	ip := manage.PublicIPv4()
	if ip != "" && ip != "-" {
		ipStore.Store(ip)
		return ip
	}
	return "-"
}

func refreshLabel() string {
	h := schedule.AutoRefreshHours()
	if h <= 0 {
		return "disabled"
	}
	return fmt.Sprintf("every %dh", h)
}

// manageMenu is menu item 3.
func manageMenu() {
	for {
		tui.Clear()
		tui.Colorize(tui.Cyan, "Manage", true)
		fmt.Println()
		idx := tui.Choose("Choose:", []string{
			"Manage Tunnels",
			"Status",
			"Restart ALL",
			"Backup & Restore",
		})
		switch idx {
		case 0:
			manage.ManageTunnels()
		case 1:
			manage.StatusLive()
		case 2:
			ok, failed := manage.RestartAll()
			tui.Success(fmt.Sprintf("Restarted %d tunnels (%d failed).", ok, failed))
			tui.PressEnter()
		case 3:
			backupMenu()
		default:
			return
		}
	}
}

// backupMenu creates or restores a full configuration backup (all tunnels, the
// web-panel password, Telegram settings, certificates and the auto-refresh
// schedule) as a single portable .tar.gz archive.
func backupMenu() {
	for {
		tui.Clear()
		tui.Colorize(tui.Cyan, "Backup & Restore", true)
		fmt.Println()
		tui.Info("A backup bundles every tunnel, the web-panel password, Telegram")
		tui.Info("settings, TLS certs and the auto-refresh schedule into one file.")
		fmt.Println()

		idx := tui.Choose("Choose:", []string{
			"Create a backup file",
			"Restore from a backup file",
		})
		switch idx {
		case 0:
			createBackup()
		case 1:
			restoreBackup()
		default:
			return
		}
	}
}

// createBackup writes a timestamped archive to /root and shows its path.
func createBackup() {
	dir := tui.PromptDefault("Save the backup in which directory", "/root")
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

// restoreBackup restores tunnels and settings from a chosen archive.
func restoreBackup() {
	path := tui.Prompt("Path to the backup .tar.gz file: ")
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

// afterSetupWeb ensures the web panel is running after a tunnel is created and
// shows the user its URL and login code.
func afterSetupWeb() {
	cfg, err := webui.EnsureRunning()
	if err != nil {
		tui.Warn("Web panel could not start: " + err.Error())
		tui.PressEnter()
		return
	}
	showWebCreds(cfg)
}

// showWebCreds prints the web panel URL and 8-digit login code.
func showWebCreds(cfg webui.Config) {
	tui.Clear()
	tui.Success("Web panel is live.")
	fmt.Println()
	ip := resolveServerIP()
	tui.Info(fmt.Sprintf("  URL:    http://%s:%d", ip, cfg.Port))
	tui.Info(fmt.Sprintf("  Code:   %s   (8-digit login)", cfg.Password))
	fmt.Println()
	tui.Warn("Keep this code private. Regenerate it any time from the Web Panel menu.")
	tui.PressEnter()
}

// setCustomPassword prompts for a custom web-panel password and applies it.
func setCustomPassword() {
	tui.Clear()
	tui.Colorize(tui.Cyan, "Set a custom web panel password", true)
	fmt.Println()
	pw := tui.Prompt("New password (4–128 chars, letters/digits/symbols): ")
	if len(pw) < 4 || len(pw) > 128 {
		tui.Error("Password must be between 4 and 128 characters.")
		tui.PressEnter()
		return
	}
	confirm := tui.Prompt("Repeat the password: ")
	if pw != confirm {
		tui.Error("Passwords do not match.")
		tui.PressEnter()
		return
	}
	c, err := webui.SetPassword(pw)
	if err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Password updated.")
	showWebCreds(c)
}

// webPanelMenu manages the web panel (item under Manage).
func webPanelMenu() {
	for {
		tui.Clear()
		tui.Colorize(tui.Cyan, "Web Panel", true)
		fmt.Println()
		cfg := webui.Load()
		if webui.Running() {
			tui.Info(fmt.Sprintf("Status: running on port %d", cfg.Port))
		} else {
			tui.Warn("Status: stopped")
		}
		fmt.Println()

		idx := tui.Choose("Choose:", []string{
			"Show link & login code",
			"Enable / Start panel",
			"Regenerate login code (random 8-digit)",
			"Set a custom password",
			"Restart panel",
			"Stop panel",
		})
		switch idx {
		case 0:
			showWebCreds(webui.Load())
		case 1:
			c, err := webui.EnsureRunning()
			if err != nil {
				tui.Error("Failed: " + err.Error())
				tui.PressEnter()
			} else {
				showWebCreds(c)
			}
		case 2:
			c, err := webui.RegeneratePassword()
			if err != nil {
				tui.Error("Failed: " + err.Error())
				tui.PressEnter()
			} else {
				tui.Success("New login code generated.")
				showWebCreds(c)
			}
		case 3:
			setCustomPassword()
		case 4:
			if err := manage.RestartService(app.WebUIService); err != nil {
				tui.Error("Failed: " + err.Error())
			} else {
				tui.Success("Web panel restarted.")
			}
			tui.PressEnter()
		case 5:
			if err := webui.Disable(); err != nil {
				tui.Error("Failed: " + err.Error())
			} else {
				tui.Success("Web panel stopped.")
			}
			tui.PressEnter()
		default:
			return
		}
	}
}

// autoRefreshMenu is menu item 4.
func autoRefreshMenu() {
	tui.Clear()
	tui.Colorize(tui.Cyan, "Auto Refresh Schedule", true)
	fmt.Println()
	tui.Info(fmt.Sprintf("Current interval: %s", refreshLabel()))
	fmt.Println()
	hours := tui.PromptInt("Auto refresh interval in hours (0 to disable)", schedule.AutoRefreshHours())
	if err := schedule.SetAutoRefresh(hours); err != nil {
		tui.Error("Failed to update schedule: " + err.Error())
	} else if hours <= 0 {
		tui.Success("Auto refresh disabled.")
	} else {
		tui.Success(fmt.Sprintf("All tunnels will restart every %d hour(s).", hours))
	}
	tui.PressEnter()
}

// optimizeMenu is menu item 6.
func optimizeMenu() {
	tui.Clear()
	tui.Colorize(tui.Cyan, "Optimize — kernel & network tuning (BBR, buffers, limits)", true)
	fmt.Println()
	if !tui.Confirm("Apply system-wide network optimizations now", true) {
		return
	}
	fmt.Println()
	optimize.Apply(func(line string) { tui.Info("• " + line) })
	fmt.Println()
	tui.Warn("A reboot is recommended for file-limit changes to fully apply.")
	tui.PressEnter()
}

// telegramMenu is menu item 7.
func telegramMenu() {
	tui.Clear()
	tui.Colorize(tui.Cyan, "Telegram Bot", true)
	fmt.Println()

	cfg := telegram.Load()
	if cfg.Token != "" {
		tui.Info(fmt.Sprintf("Configured — reports every %d hour(s).", telegram.IntervalHours()))
	} else {
		tui.Info("Not configured yet.")
	}
	fmt.Println()

	idx := tui.Choose("Choose:", []string{
		"Configure / Update bot",
		"Send a test report now",
		"Disable reports",
	})
	switch idx {
	case 0:
		configureTelegram(cfg)
	case 1:
		if err := telegram.SendStatusNow(); err != nil {
			tui.Error("Failed: " + err.Error())
		} else {
			tui.Success("Report sent.")
		}
		tui.PressEnter()
	case 2:
		if err := telegram.Disable(); err != nil {
			tui.Error("Failed: " + err.Error())
		} else {
			tui.Success("Telegram reports disabled.")
		}
		tui.PressEnter()
	}
}

func configureTelegram(cfg telegram.Config) {
	tui.Info("Get a bot token from @BotFather and your numeric user id from @userinfobot.")
	fmt.Println()
	cfg.Token = tui.PromptDefault("Bot token", cfg.Token)
	cfg.AdminID = tui.PromptDefault("Admin user id", cfg.AdminID)

	if cfg.Token == "" || cfg.AdminID == "" {
		tui.Error("Token and admin id are required.")
		tui.PressEnter()
		return
	}

	// If this server can't reach Telegram (e.g. Iran), relay through a tunnel
	// whose peer (e.g. kharej) can.
	fmt.Println()
	if tui.Confirm("Can THIS server reach Telegram directly", true) {
		cfg.ViaTunnel = ""
	} else {
		tunnels := manage.List()
		if len(tunnels) == 0 {
			tui.Error("No tunnels to relay through — create a tunnel first.")
			tui.PressEnter()
			return
		}
		labels := make([]string, len(tunnels))
		for i, t := range tunnels {
			labels[i] = fmt.Sprintf("%s  [%s %s]", t.Name, t.Role, t.Transport)
		}
		idx := tui.Choose("Relay Telegram through which tunnel (its peer must reach Telegram):", labels)
		if idx < 0 {
			return
		}
		cfg.ViaTunnel = tunnels[idx].Name
		tui.Info("Setting up a SOCKS relay through tunnel " + cfg.ViaTunnel + "...")
		port, err := manage.EnsureSocksPort(cfg.ViaTunnel)
		if err != nil {
			tui.Error("Could not set up relay: " + err.Error())
			tui.PressEnter()
			return
		}
		cfg.SocksPort = port
		tui.Success(fmt.Sprintf("Relay ready — port %d added to the tunnel.", port))
		tui.Warn("Reconnect/restart the CLIENT tunnel once so it picks up the new port.")
	}

	fmt.Println()
	cfg.IntervalHours = tui.PromptInt("Send status every N hours", maxInt(cfg.IntervalHours, 6))
	if err := telegram.Configure(cfg); err != nil {
		tui.Error("Failed to save: " + err.Error())
		tui.PressEnter()
		return
	}
	if err := telegram.SendTest(cfg); err != nil {
		tui.Warn("Saved, but test message failed: " + err.Error())
	} else {
		tui.Success("Saved and test message delivered.")
	}
	tui.PressEnter()
}

// updateMenu checks GitHub for a newer version and updates in place.
func updateMenu() {
	tui.Clear()
	tui.Colorize(tui.Cyan, "Update Backpack", true)
	fmt.Println()
	tui.Info("Checking GitHub for updates...")

	available, summary, err := manage.CheckUpdate()
	if err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	if !available {
		tui.Success(summary)
		tui.PressEnter()
		return
	}

	tui.Warn(summary)
	if !tui.Confirm("Download and install the update now", true) {
		return
	}
	fmt.Println()
	if err := manage.ApplyUpdate(func(l string) { tui.Info("• " + l) }); err != nil {
		tui.Error("Update failed: " + err.Error())
	} else {
		tui.Success("Backpack updated successfully.")
	}
	tui.PressEnter()
}

// uninstallMenu is menu item 9.
func uninstallMenu() {
	tui.Clear()
	tui.Colorize(tui.Red, "Uninstall Backpack", true)
	fmt.Println()
	tui.Warn("This removes EVERYTHING: all tunnels, services, schedules, configs,")
	tui.Warn("the backpack binary, AND the backpack source folder itself.")
	if !tui.Confirm("Are you absolutely sure", false) {
		return
	}

	// Capture the repo path before we delete the config that records it.
	repo := manage.InstallPath()

	for _, t := range manage.List() {
		_ = manage.Delete(t.Name)
	}
	_ = webui.Disable()
	_ = schedule.SetAutoRefresh(0)
	_ = telegram.Disable()
	os.RemoveAll(app.ConfigDir)
	if err := os.Remove(app.BinPath); err != nil {
		tui.Warn("Could not remove binary at " + app.BinPath + " — remove it manually.")
	}
	if repo != "" && repo != "/" && repo != os.Getenv("HOME") {
		if err := os.RemoveAll(repo); err != nil {
			tui.Warn("Could not remove source folder " + repo + " — remove it manually.")
		} else {
			tui.Info("Removed source folder: " + repo)
		}
	}
	tui.Success("Backpack has been completely uninstalled. Goodbye!")
	os.Exit(0)
}

func requireRoot() {
	if os.Geteuid() != 0 {
		tui.Error("Backpack must be run as root (use: sudo backpack).")
		os.Exit(1)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
