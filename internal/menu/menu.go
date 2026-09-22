// Package menu implements the interactive backpack CLI shown when the binary
// is run without a config file.
package menu

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/schedule"
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

	// The same for the tunnels' own units: a tunnel created by an older version
	// keeps that version's unit file, which is how servers stayed on systemd's
	// default open-file ceiling long after the template had been raised.
	// Nothing is restarted; each tunnel picks its unit up when it next starts.
	manage.EnsureUnits()

	// The watchdog, the Telegram bot and the alerts run in their own service so
	// they survive the panel being stopped. Installing it here is also how an
	// install that predates the service picks it up.
	if err := manage.EnsureMonitorService(); err != nil {
		tui.Warn("Monitor service could not start: " + err.Error())
		tui.PressEnter()
	}

	go resolveServerIP()

	// Look for a newer release in the background. The menu itself only ever
	// reads the cached answer, so a slow or blocked GitHub cannot delay a
	// redraw — the notice simply appears once the check comes back.
	go manage.RefreshUpdateCheckIfStale(6 * time.Hour)

	for {
		tui.Clear()
		tui.SetAttribution(app.Attribution)
		tui.Logo(app.Version)
		printUpdateBanner()
		tui.Rule()
		printMenu()

		switch tui.Prompt("Select an option: ") {
		// Both entries ask which direction the tunnel should be built in, and
		// a reverse one is then built by exactly the code that has always
		// built it. See manage.SetupIran.
		case "1":
			manage.SetupIran()
		case "2":
			manage.SetupKharej()
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

// printUpdateBanner shows a one-line notice when a newer release exists. It
// reads the cache only, so it costs nothing and prints nothing until the
// background check has an answer.
func printUpdateBanner() {
	tag, ok := manage.UpdateAvailable()
	if !ok {
		return
	}
	fmt.Printf("  %s⬆ %s is available%s %s— option 8 to update safely%s\n",
		tui.Bold+tui.Red, tag, tui.Reset, tui.Gray, tui.Reset)
}

// printMenu renders the main menu: red numbers, white titles, gray descriptions.
func printMenu() {
	fmt.Println()
	menuItem(1, "Setup Iran", "the server your users connect to — it exposes the ports")
	menuItem(2, "Setup Kharej", "the server abroad — it holds the real service")
	menuItem(3, "Manage", "tunnels, ports, transport, status, health check")
	menuItem(4, "Backup & Restore", "save or restore the full configuration")
	menuItem(5, "Web Panel", "monitoring web UI — link, login code, port")
	menuItem(6, "Optimize", "kernel & network tuning — BBR, buffers, limits")
	menuItem(7, "Telegram Bot", "status reports, relayed through a tunnel")
	updateDesc := "safe update with automatic rollback"
	if tag, ok := manage.UpdateAvailable(); ok {
		updateDesc = tag + " is out — safe update with automatic rollback"
	}
	menuItem(8, "Update", updateDesc)
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
