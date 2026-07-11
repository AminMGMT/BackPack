// Package menu implements the interactive backpack CLI shown when the binary
// is run without a config file.
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

// Run starts the interactive menu loop.
func Run() {
	requireRoot()
	for {
		tui.Clear()
		tui.Logo(app.Version)
		printHeader()

		fmt.Println()
		fmt.Println("  1. Setup Server")
		fmt.Println("  2. Setup Client")
		fmt.Println("  3. Web Panel")
		fmt.Println("  4. Manage")
		fmt.Println("  5. Auto Refresh Schedule")
		fmt.Println("  6. Status")
		fmt.Println("  7. Optimize")
		fmt.Println("  8. Telegram Bot")
		fmt.Println("  9. Update")
		fmt.Println(" 10. Uninstall")
		fmt.Println(" 11. Exit")
		fmt.Println()

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

// printHeader shows a quick summary line under the logo.
func printHeader() {
	tui.Rule()
	tunnels := manage.List()
	running := 0
	for _, t := range tunnels {
		if manage.IsActive(t.Service) {
			running++
		}
	}
	fmt.Printf("%sTunnels:%s %d total, %s%d running%s   %sAuto-refresh:%s %s\n",
		tui.Cyan, tui.Reset, len(tunnels),
		tui.Green, running, tui.Reset,
		tui.Cyan, tui.Reset, refreshLabel())
	web := tui.Color(tui.Red, "disabled")
	if webui.Running() {
		web = tui.Color(tui.Green, fmt.Sprintf("running on :%d", webui.Load().Port))
	}
	fmt.Printf("%sWeb Panel:%s %s\n", tui.Cyan, tui.Reset, web)
	tui.Rule()
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
		default:
			return
		}
	}
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
	ip := manage.PublicIPv4()
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
