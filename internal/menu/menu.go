// Package menu implements the interactive backpack CLI shown when the binary
// is run without a config file.
package menu

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/optimize"
	"github.com/backpack/backpack/internal/schedule"
	"github.com/backpack/backpack/internal/telegram"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/webui"
)

// ipStore caches the server's public IPv4 so menus never block on a lookup.
var ipStore atomic.Value // holds string

// Run starts the interactive menu loop.
func Run() {
	requireRoot()

	// Bring the monitoring web panel up in the background and start resolving
	// the public IP (shown inside the Web Panel section).
	if _, err := webui.EnsureRunning(); err != nil {
		tui.Warn("Web panel could not start: " + err.Error())
		tui.PressEnter()
	}
	go resolveServerIP()

	for {
		tui.Clear()
		tui.Logo(app.Version)
		tui.Rule()
		printMenu()

		switch tui.Prompt("Select an option: ") {
		case "1":
			manage.SetupServer()
		case "2":
			manage.SetupClient()
		case "3":
			manageMenu()
		case "4":
			backupMenu()
		case "5":
			webPanelMenu()
		case "6":
			optimizeMenu()
		case "7":
			telegramMenu()
		case "8":
			updateMenu()
		case "9":
			uninstallMenu()
		case "10", "0":
			tui.Info("Goodbye!")
			return
		default:
			tui.Error("Invalid option.")
			tui.PressEnter()
		}
	}
}

// printMenu renders the main menu: red numbers, white titles, gray descriptions.
func printMenu() {
	fmt.Println()
	menuItem(1, "Setup Server", "Iran side — exposes ports to users")
	menuItem(2, "Setup Client", "Kharej side — dials out to the Iran server")
	menuItem(3, "Manage", "tunnels, ports, transport, status, health check")
	menuItem(4, "Backup & Restore", "save or restore the full configuration")
	menuItem(5, "Web Panel", "monitoring web UI — link, login code, port")
	menuItem(6, "Optimize", "kernel & network tuning — BBR, buffers, limits")
	menuItem(7, "Telegram Bot", "status reports, relayed through a tunnel")
	menuItem(8, "Update", "safe update with automatic rollback")
	menuItem(9, "Uninstall", "remove everything")
	menuItem(10, "Exit", "")
	fmt.Println()
}

// menuItem prints one aligned, colored menu row.
func menuItem(n int, title, desc string) {
	num := tui.Color(tui.Red, fmt.Sprintf("%2d)", n))
	if desc == "" {
		fmt.Printf("  %s %s%-18s%s\n", num, tui.Bold+tui.White, title, tui.Reset)
		return
	}
	fmt.Printf("  %s %s%-18s%s %s%s%s\n",
		num, tui.Bold+tui.White, title, tui.Reset, tui.Gray, desc, tui.Reset)
}

// cachedServerIP returns the resolved public IPv4 if known, otherwise a
// placeholder — it never blocks, so it's safe for redrawn screens.
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

// manageMenu is main-menu item 3.
func manageMenu() {
	for {
		tui.Clear()
		idx := tui.ChooseOpt("Manage", []tui.Option{
			{Title: "Manage Tunnels", Desc: "edit ports & transport, start/stop, live log, delete"},
			{Title: "Status", Desc: "live tunnel table"},
			{Title: "Health Check", Desc: "find problems and get a fix for each one"},
			{Title: "Restart ALL", Desc: "restart every tunnel at once"},
			{Title: "Auto Refresh", Desc: "restart all tunnels every N hours — " + refreshLabel()},
			{Title: "File Locations", Desc: "where every config, service and backup lives"},
		})
		switch idx {
		case 0:
			manage.ManageTunnels()
		case 1:
			manage.StatusLive()
		case 2:
			manage.HealthCheck()
		case 3:
			ok, failed := manage.RestartAll()
			tui.Success(fmt.Sprintf("Restarted %d tunnels (%d failed).", ok, failed))
			tui.PressEnter()
		case 4:
			autoRefreshMenu()
		case 5:
			manage.FileLocations()
		default:
			return
		}
	}
}

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

		idx := tui.ChooseOpt("Choose:", []tui.Option{
			{Title: "Create a backup file", Desc: "saved into " + app.BackupDir},
			{Title: "Restore from a backup file", Desc: "pick one from the folder or enter a path"},
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

// panelHeader prints the web panel's live status, URL and login code — shown
// at the top of the Web Panel section.
func panelHeader(cfg webui.Config) {
	tui.Rule()
	if webui.Running() {
		fmt.Printf("  %sStatus%s      %s● running%s\n", tui.Gray, tui.Reset, tui.Bold+tui.White, tui.Reset)
		fmt.Printf("  %sWeb Panel%s   %shttp://%s:%d%s\n", tui.Gray, tui.Reset,
			tui.Bold+tui.White, cachedServerIP(), cfg.Port, tui.Reset)
		fmt.Printf("  %sLogin code%s  %s%s%s\n", tui.Gray, tui.Reset, tui.Bold+tui.Red, cfg.Password, tui.Reset)
	} else {
		fmt.Printf("  %sStatus%s      %s○ stopped%s %s(use Restart panel to start it)%s\n",
			tui.Gray, tui.Reset, tui.Red, tui.Reset, tui.Gray, tui.Reset)
	}
	tui.Rule()
}

// webPanelMenu is main-menu item 5 — the monitoring web UI.
func webPanelMenu() {
	for {
		tui.Clear()
		tui.Title("Web Panel")
		tui.Warn("Monitoring-only dashboard — recommended on the IRAN server.")
		fmt.Println()
		cfg := webui.Load()
		panelHeader(cfg)
		fmt.Println()

		idx := tui.ChooseOpt("Choose:", []tui.Option{
			{Title: "Change panel port", Desc: fmt.Sprintf("current: %d", cfg.Port)},
			{Title: "Regenerate login code", Desc: "new random 8-digit code"},
			{Title: "Set a custom password", Desc: "replace the login code with your own"},
			{Title: "Restart panel", Desc: "also starts it when stopped"},
			{Title: "Stop panel", Desc: "disable the web UI"},
		})
		switch idx {
		case 0:
			changePanelPort()
		case 1:
			c, err := webui.RegeneratePassword()
			if err != nil {
				tui.Error("Failed: " + err.Error())
			} else {
				tui.Success("New login code generated: " + c.Password)
			}
			tui.PressEnter()
		case 2:
			setCustomPassword()
		case 3:
			if _, err := webui.EnsureRunning(); err != nil {
				tui.Error("Failed: " + err.Error())
			} else if err := manage.RestartService(app.WebUIService); err != nil {
				tui.Error("Failed: " + err.Error())
			} else {
				tui.Success("Web panel restarted.")
			}
			tui.PressEnter()
		case 4:
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

// changePanelPort moves the web panel to a different port and restarts it.
func changePanelPort() {
	fmt.Println()
	cur := webui.Load().Port
	p := tui.PromptInt("New panel port", cur)
	if p == cur {
		return
	}
	if p < 1 || p > 65535 {
		tui.Error("Invalid port — must be between 1 and 65535.")
		tui.PressEnter()
		return
	}
	if manage.PortInUse(strconv.Itoa(p)) {
		tui.Error(fmt.Sprintf("Port %d is already in use on this machine.", p))
		tui.PressEnter()
		return
	}
	if _, err := webui.SetPort(p); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success(fmt.Sprintf("Panel moved to port %d — the panel was restarted.", p))
	tui.PressEnter()
}

// setCustomPassword prompts for a custom web-panel password and applies it.
func setCustomPassword() {
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
	if _, err := webui.SetPassword(pw); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Password updated.")
	tui.PressEnter()
}

// autoRefreshMenu lives under Manage.
func autoRefreshMenu() {
	tui.Clear()
	tui.Title("Auto Refresh Schedule")
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

// optimizeMenu is main-menu item 6.
func optimizeMenu() {
	tui.Clear()
	tui.Title("Optimize — kernel & network tuning (BBR, buffers, limits)")
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

// telegramMenu is main-menu item 7.
func telegramMenu() {
	tui.Clear()
	tui.Title("Telegram Bot")
	fmt.Println()

	cfg := telegram.Load()
	if cfg.Token != "" {
		tui.Info(fmt.Sprintf("Configured — reports every %d hour(s).", telegram.IntervalHours()))
		if cfg.ViaTunnel != "" {
			tui.Warn("Relayed through tunnel " + cfg.ViaTunnel + " (works from Iran).")
		}
	} else {
		tui.Info("Not configured yet.")
	}
	fmt.Println()

	idx := tui.ChooseOpt("Choose:", []tui.Option{
		{Title: "Configure / Update bot", Desc: "token, admin id, tunnel relay"},
		{Title: "Send a test report now", Desc: "verify the bot works"},
		{Title: "Disable reports", Desc: "stop the scheduled reports"},
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

// configureTelegram sets up the bot. On an Iran server Telegram is blocked, so
// the primary path relays traffic through a tunnel: backpack exposes a random
// port on the chosen tunnel that maps to the peer's built-in SOCKS5 proxy and
// sends every bot request through it — this works 100% from Iran.
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

	fmt.Println()
	tunnels := manage.List()
	if len(tunnels) == 0 {
		tui.Warn("No tunnels yet. On an IRAN server the bot can only reach Telegram")
		tui.Warn("through a tunnel relay — create a tunnel first for reliable delivery.")
		if !tui.Confirm("Send DIRECTLY instead (only works where Telegram is reachable)", false) {
			return
		}
		cfg.ViaTunnel = ""
	} else {
		opts := make([]tui.Option, 0, len(tunnels)+1)
		for _, t := range tunnels {
			opts = append(opts, tui.Option{
				Title: "Relay via " + t.Name,
				Desc:  fmt.Sprintf("%s %s — recommended, works from Iran", t.Role, t.Transport),
			})
		}
		opts = append(opts, tui.Option{
			Title: "Direct",
			Desc:  "only if THIS server can reach Telegram (e.g. kharej)",
		})
		idx := tui.ChooseOpt("Send Telegram traffic through:", opts)
		if idx < 0 {
			return
		}
		if idx < len(tunnels) {
			cfg.ViaTunnel = tunnels[idx].Name
			tui.Info("Setting up a SOCKS5 relay through tunnel " + cfg.ViaTunnel + "...")
			port, err := manage.EnsureSocksPort(cfg.ViaTunnel)
			if err != nil {
				tui.Error("Could not set up relay: " + err.Error())
				tui.PressEnter()
				return
			}
			cfg.SocksPort = port
			tui.Success(fmt.Sprintf("Relay ready — port %d added to the tunnel.", port))
			tui.Warn("Reconnect/restart the CLIENT tunnel once so it picks up the new port.")
		} else {
			cfg.ViaTunnel = ""
		}
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

// updateMenu offers a safe update and the restore points it creates.
func updateMenu() {
	for {
		tui.Clear()
		tui.Title("Update Backpack")
		tui.Warn("Current version: " + app.Version)
		fmt.Println()

		idx := tui.ChooseOpt("Choose:", []tui.Option{
			{Title: "Check for updates", Desc: "install the latest release — safely, with automatic rollback"},
			{Title: "Restore points", Desc: "go back to a previous version if something went wrong"},
		})
		switch idx {
		case 0:
			runUpdate()
		case 1:
			restorePointMenu()
		default:
			return
		}
	}
}

// runUpdate checks for and installs a newer release. A restore point is taken
// first and the update rolls itself back if the services do not come back up.
func runUpdate() {
	tui.Clear()
	tui.Title("Check for updates")
	fmt.Println()
	tui.Info("Checking GitHub releases (direct, then tunnel relay, then mirrors)...")

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
	fmt.Println()
	tui.Info("A restore point is saved first. If anything fails to come back up,")
	tui.Info("Backpack puts the previous version back automatically.")
	fmt.Println()
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
	} else {
		tui.Success("Rolled back to " + chosen.Meta.Version + " successfully.")
	}
	tui.PressEnter()
}

// uninstallMenu is main-menu item 9.
func uninstallMenu() {
	tui.Clear()
	tui.Title("Uninstall Backpack")
	fmt.Println()
	tui.Warn("This removes EVERYTHING: all tunnels, services, schedules, configs,")
	tui.Warn("the backpack binary, AND the " + app.InstallDir + " folder (incl. backups).")
	if !tui.Confirm("Are you absolutely sure", false) {
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
	_ = schedule.SetAutoRefresh(0)
	_ = telegram.Disable()
	os.RemoveAll(app.ConfigDir)
	if err := os.Remove(app.BinPath); err != nil {
		tui.Warn("Could not remove binary at " + app.BinPath + " — remove it manually.")
	}
	if repo != "" && repo != "/" && repo != os.Getenv("HOME") {
		if err := os.RemoveAll(repo); err != nil {
			tui.Warn("Could not remove folder " + repo + " — remove it manually.")
		} else {
			tui.Info("Removed folder: " + repo)
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
