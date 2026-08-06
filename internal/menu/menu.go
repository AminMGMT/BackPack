// Package menu implements the interactive backpack CLI shown when the binary
// is run without a config file.
package menu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/engine"
	"github.com/backpack/backpack/internal/localproxy"
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
		tui.Logo(app.Version)
		printUpdateBanner()
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
		case "10", "11", "0":
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
	menuItem(1, "Setup Server", "Iran side — exposes ports; Direct dials Kharej")
	menuItem(2, "Setup Client", "Kharej side — Direct accepts, Reverse dials Iran")
	menuItem(3, "Manage", "instances, ports, transport, status, health check")
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

// manageMenu is main-menu item 3.
func manageMenu() {
	for {
		tui.Clear()
		idx := tui.ChooseOpt("Manage", []tui.Option{
			{Title: "Manage Tunnels", Desc: "edit ports & transport, start/stop, live log, delete"},
			{Title: "Status", Desc: "live tunnel table"},
			{Title: "Health Check", Desc: "find problems and get a fix for each one"},
			{Title: "Link Test", Desc: "measure the link and get a transport recommendation"},
			{Title: "IP Spoofing Tester", Desc: "find which forged source IPs cross the firewall (for the spoof transport)"},
			{Title: "Tunnel Metrics", Desc: "traffic, packet loss and error correction per tunnel"},
			{Title: "Restart ALL", Desc: "restart every tunnel at once"},
			{Title: "Auto Refresh", Desc: "restart all tunnels every N hours — " + refreshLabel()},
			{Title: "Built-in Proxy", Desc: "be your own SOCKS5/HTTP backend — " + proxyLabel()},
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
			manage.LinkTest()
		case 4:
			manage.SpoofTest()
		case 5:
			manage.TunnelMetrics()
		case 6:
			ok, failed := manage.RestartAll()
			tui.Success(fmt.Sprintf("Restarted %d tunnels (%d failed).", ok, failed))
			tui.PressEnter()
		case 7:
			autoRefreshMenu()
		case 8:
			builtinProxyMenu()
		case 9:
			manage.FileLocations()
		default:
			return
		}
	}
}

// proxyLabel summarises the built-in proxy for the menu row.
func proxyLabel() string {
	c := localproxy.Load()
	if !c.Enabled || !manage.ProxyRunning() {
		return "off"
	}
	return fmt.Sprintf("%s on :%d", c.Type, c.Port)
}

// builtinProxyMenu configures the optional built-in SOCKS5/HTTP proxy: this
// node becomes its own backend, so nothing separate (xray, a panel) has to be
// installed behind the tunnel. The operator picks the port — there is no
// assumed default — and forwards a tunnel port to 127.0.0.1:<that port>.
func builtinProxyMenu() {
	tui.Clear()
	tui.Title("Built-in Proxy")
	tui.Warn("This node serves its own SOCKS5 or HTTP proxy on a loopback port.")
	tui.Warn("Point a forwarded port at 127.0.0.1:<that port> and the tunnel exit")
	tui.Warn("is the proxy itself — no separate backend to install or keep running.")
	fmt.Println()

	c := localproxy.Load()
	if c.Enabled && manage.ProxyRunning() {
		tui.Info(fmt.Sprintf("Currently: %s proxy on 127.0.0.1:%d", c.Type, c.Port))
	} else {
		tui.Info("Currently: off")
	}
	fmt.Println()

	idx := tui.ChooseOpt("Choose:", []tui.Option{
		{Title: "Enable / reconfigure", Desc: "pick SOCKS5 or HTTP, a port, and optional auth"},
		{Title: "Disable", Desc: "stop and remove the proxy service"},
		{Title: "Back", Desc: ""},
	})
	switch idx {
	case 0:
		configureProxy(c)
	case 1:
		if err := manage.DisableProxyService(); err != nil {
			tui.Error("Could not disable: " + err.Error())
		} else {
			tui.Success("Built-in proxy disabled.")
		}
		tui.PressEnter()
	}
}

func configureProxy(c localproxy.Config) {
	kind := tui.ChooseOpt("Proxy type:", []tui.Option{
		{Title: "SOCKS5", Desc: "works for most apps; carries UDP too"},
		{Title: "HTTP", Desc: "for clients that only take an HTTP proxy (browsers)"},
	})
	switch kind {
	case 0:
		c.Type = localproxy.SOCKS5
	case 1:
		c.Type = localproxy.HTTP
	default:
		return
	}

	// The operator chooses the port; nothing is assumed.
	c.Port = tui.PromptInt("Port to listen on (loopback)", c.Port)
	if c.Port <= 0 || c.Port > 65535 {
		tui.Error("Port must be between 1 and 65535.")
		tui.PressEnter()
		return
	}

	if tui.Confirm("Require a username/password", c.Username != "") {
		c.Username = tui.PromptDefault("Username", c.Username)
		c.Password = tui.PromptDefault("Password", c.Password)
	} else {
		c.Username, c.Password = "", ""
		tui.Warn("No auth — safe here: the proxy binds loopback and is only")
		tui.Warn("reachable through the token-authenticated tunnel.")
	}

	if err := manage.EnableProxyService(c); err != nil {
		tui.Error("Could not enable: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success(fmt.Sprintf("%s proxy running on 127.0.0.1:%d.", c.Type, c.Port))
	tui.Info(fmt.Sprintf("Now forward a tunnel port to 127.0.0.1:%d "+
		"(e.g. in Setup Server: 443=127.0.0.1:%d).", c.Port, c.Port))
	tui.PressEnter()
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
		host := cachedServerIP()
		if cfg.TLSDomain != "" {
			host = cfg.TLSDomain
		}
		fmt.Printf("  %sWeb Panel%s   %s%s://%s:%d%s\n", tui.Gray, tui.Reset,
			tui.Bold+tui.White, cfg.Scheme(), host, cfg.Port, tui.Reset)
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
			{Title: "Certificate", Desc: panelCertDesc(cfg)},
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
			panelCertMenu(cfg)
		case 4:
			if _, err := webui.EnsureRunning(); err != nil {
				tui.Error("Failed: " + err.Error())
			} else if err := manage.RestartService(app.WebUIService); err != nil {
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

// panelCertDesc summarises how the panel is reached, for the menu line.
func panelCertDesc(cfg webui.Config) string {
	switch {
	case !cfg.HTTPS:
		return "plain HTTP — no certificate"
	case cfg.TLSDomain != "":
		return "Let's Encrypt for " + cfg.TLSDomain + " (renews itself)"
	default:
		return "HTTPS with a self-signed certificate"
	}
}

// panelCertMenu chooses how the panel presents itself.
//
// The two certificate options are not interchangeable. A self-signed one works
// anywhere, including on a bare IP, which is where most of these panels live —
// and every browser will warn about it once, because that is exactly what a
// self-signed certificate is for. Let's Encrypt issues one browsers trust, but
// only for a domain name that resolves to this server, and reaching it needs
// port 80 open for the challenge.
//
// Switching changes the address people have bookmarked, so it says so.
func panelCertMenu(cfg webui.Config) {
	tui.Clear()
	tui.Title("Panel certificate")
	fmt.Println()
	tui.Info("Currently: " + panelCertDesc(cfg))
	fmt.Println()

	idx := tui.ChooseOpt("How should the panel be served?", []tui.Option{
		{Title: "Plain HTTP", Desc: "no certificate — the default"},
		{Title: "HTTPS, self-signed", Desc: "works on a bare IP; the browser warns once"},
		{Title: "HTTPS, Let's Encrypt", Desc: "trusted certificate — needs a domain and port 80"},
	})

	switch idx {
	case 0:
		cfg.HTTPS, cfg.TLSDomain, cfg.TLSEmail = false, "", ""
	case 1:
		cfg.HTTPS, cfg.TLSDomain, cfg.TLSEmail = true, "", ""
	case 2:
		fmt.Println()
		tui.Warn("The domain must already point at this server, and port 80 must be")
		tui.Warn("reachable — Let's Encrypt uses it to verify the name is yours.")
		fmt.Println()
		domain := strings.TrimSpace(tui.PromptDefault("Domain (e.g. panel.example.com)", cfg.TLSDomain))
		if domain == "" {
			tui.Warn("No domain given — nothing changed.")
			tui.PressEnter()
			return
		}
		email := strings.TrimSpace(tui.PromptDefault("Email for expiry warnings (optional)", cfg.TLSEmail))
		cfg.HTTPS, cfg.TLSDomain, cfg.TLSEmail = true, domain, email
	default:
		return
	}

	if err := webui.Save(cfg); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	if err := manage.RestartService(app.WebUIService); err != nil {
		tui.Error("Saved, but the panel would not restart: " + err.Error())
		tui.PressEnter()
		return
	}

	fmt.Println()
	tui.Success("Saved. The panel is now on " + panelCertDesc(cfg) + ".")
	host := cachedServerIP()
	if cfg.TLSDomain != "" {
		host = cfg.TLSDomain
	}
	tui.Warn(fmt.Sprintf("The address changed — use %s://%s:%d", cfg.Scheme(), host, cfg.Port))
	if cfg.HTTPS && cfg.TLSDomain != "" {
		tui.Warn("The first request takes a few seconds while the certificate is issued.")
	}
	tui.PressEnter()
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
		tui.Info("Relay                 : " + telegram.RelayStatus())
		tui.Info("Alerts                : " + alertSummaryLine(cfg.Alerts))
	} else {
		tui.Info("Not configured yet.")
	}
	fmt.Println()

	idx := tui.ChooseOpt("Choose:", []tui.Option{
		{Title: "Configure / Update bot", Desc: "token, admin id, tunnel relay"},
		{Title: "Alerts", Desc: "warn when CPU, memory, disk or a tunnel goes bad"},
		{Title: "Diagnose relay", Desc: "find which hop is broken when messages fail"},
		{Title: "Send a test report now", Desc: "verify the bot works"},
		{Title: "Disable reports", Desc: "stop the scheduled reports"},
	})
	switch idx {
	case 0:
		configureTelegram(cfg)
	case 1:
		configureAlerts(cfg)
	case 2:
		diagnoseRelay()
	case 3:
		if err := telegram.SendStatusNow(); err != nil {
			tui.Error("Failed: " + err.Error())
		} else {
			tui.Success("Report sent.")
		}
		tui.PressEnter()
	case 4:
		if err := telegram.Disable(); err != nil {
			tui.Error("Failed: " + err.Error())
		} else {
			tui.Success("Telegram reports disabled.")
		}
		tui.PressEnter()
	}
}

// alertSummaryLine renders the alert state as one line for the menu header.
func alertSummaryLine(a telegram.AlertConfig) string {
	if !a.Enabled {
		return "off"
	}
	parts := []string{}
	if a.CPUPercent > 0 {
		parts = append(parts, fmt.Sprintf("cpu %d%%", a.CPUPercent))
	}
	if a.MemPercent > 0 {
		parts = append(parts, fmt.Sprintf("ram %d%%", a.MemPercent))
	}
	if a.DiskPercent > 0 {
		parts = append(parts, fmt.Sprintf("disk %d%%", a.DiskPercent))
	}
	if a.TunnelDown {
		parts = append(parts, "tunnel up/down")
	}
	if a.NewRelease {
		parts = append(parts, "new release")
	}
	if len(parts) == 0 {
		return "on, but nothing is being watched"
	}
	return "on — " + strings.Join(parts, ", ")
}

// configureAlerts edits the alert thresholds.
func configureAlerts(cfg telegram.Config) {
	tui.Clear()
	tui.Title("Alerts")
	fmt.Println()
	tui.Warn("The bot messages you when a threshold is crossed, and again when")
	tui.Warn("it recovers. A value sitting on the line only reports once.")
	tui.Warn("Enter 0 for a threshold to stop watching it.")
	fmt.Println()

	if cfg.Token == "" {
		tui.Error("Configure the bot first — there is nowhere to send an alert.")
		tui.PressEnter()
		return
	}

	a := cfg.Alerts
	a.Enabled = tui.Confirm("Send alerts", a.Enabled)
	if a.Enabled {
		a.CPUPercent = tui.PromptInt("Processor threshold %", a.CPUPercent)
		a.MemPercent = tui.PromptInt("Memory threshold %", a.MemPercent)
		a.DiskPercent = tui.PromptInt("Disk threshold %", a.DiskPercent)
		a.TunnelDown = tui.Confirm("Alert when a tunnel goes down or comes back", a.TunnelDown)
		a.NewRelease = tui.Confirm("Tell me when a new Backpack version is released", a.NewRelease)
		a.CheckSeconds = tui.PromptInt("Check every (seconds)", a.CheckSeconds)
		a.CooldownMinutes = tui.PromptInt("Repeat a standing alert every (minutes)", a.CooldownMinutes)
	}

	cfg.Alerts = a
	if err := telegram.Save(cfg); err != nil {
		tui.Error("Failed to save: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Alert settings saved.")
	fmt.Println()
	tui.Info(a.Summary())
	fmt.Println()
	tui.Warn("Watched by the backpack-monitor service, which runs on its own —")
	tui.Warn("alerts keep working even with the web panel stopped.")
	tui.PressEnter()
}

// configureTelegram sets up the bot. On an Iran server Telegram is blocked, so
// the primary path relays traffic through a tunnel: backpack forwards a
// loopback port on the chosen tunnel straight to api.telegram.org and sends
// every bot request through it, with the peer making the outbound connection.
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
		// Automatic first, and the default. Pinning a tunnel means the bot goes
		// silent exactly when that tunnel drops — which is the moment its
		// warnings matter most.
		opts := []tui.Option{{
			Title: "Automatic (recommended)",
			Desc:  "picks a connected tunnel and switches by itself if it drops",
		}}
		for _, t := range tunnels {
			opts = append(opts, tui.Option{
				Title: "Always use " + t.Name,
				Desc:  fmt.Sprintf("%s %s — pinned; the bot goes quiet if it drops", t.Role, t.Transport),
			})
		}
		opts = append(opts, tui.Option{
			Title: "Direct",
			Desc:  "only if THIS server can reach Telegram (e.g. kharej)",
		})

		idx := tui.ChooseOpt("Send Telegram traffic through:", opts)
		switch {
		case idx < 0:
			return

		case idx == 0:
			cfg.ViaTunnel = telegram.AutoRelay
			cfg.SocksPort = 0 // resolved per request
			tui.Info("Preparing a relay on a connected tunnel...")
			if name, port, err := telegram.PrepareAutoRelay(); err != nil {
				tui.Warn("Could not prepare one yet: " + err.Error())
				tui.Warn("The bot will keep trying as tunnels come up.")
			} else {
				tui.Success(fmt.Sprintf("Relay ready on %s (port %d).", name, port))
				tui.Warn("Restart the CLIENT side of that tunnel once so it picks up the port.")
			}

		case idx <= len(tunnels):
			cfg.ViaTunnel = tunnels[idx-1].Name
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

		default:
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
		tui.Warn("Release channel : " + manage.ChannelLabel())
		fmt.Println()

		idx := tui.ChooseOpt("Choose:", []tui.Option{
			{Title: "Check for updates", Desc: "install the latest release — safely, with automatic rollback"},
			{Title: "Restore points", Desc: "go back to a previous version if something went wrong"},
			{Title: "Release channel", Desc: "stable releases only, or also test pre-releases"},
		})
		switch idx {
		case 0:
			runUpdate()
		case 1:
			restorePointMenu()
		case 2:
			channelMenu()
		default:
			return
		}
	}
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

	var cleanupFailures []string
	for _, t := range manage.List() {
		if err := manage.Delete(t.Name); err != nil {
			cleanupFailures = append(cleanupFailures, t.Name+": "+err.Error())
		}
	}
	if err := engine.CleanupOrphans(context.Background(), app.ConfigDir, true); err != nil {
		cleanupFailures = append(cleanupFailures, "orphan netfilter cleanup: "+err.Error())
	}
	if len(cleanupFailures) > 0 {
		tui.Error("Uninstall stopped because owned netfilter rules could not be safely cleaned:")
		for _, failure := range cleanupFailures {
			fmt.Println("  " + failure)
		}
		tui.Warn("Configs and the binary were kept so cleanup can be retried.")
		tui.PressEnter()
		return
	}
	_ = webui.Disable()
	_ = manage.DisableMonitorService()
	_ = schedule.SetAutoRefresh(0)
	_ = telegram.Disable()
	if err := engine.RemoveRuntimeArtifacts(); err != nil {
		tui.Warn("Could not remove the netfilter runtime lock: " + err.Error())
	}
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
