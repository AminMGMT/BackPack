// The Telegram screen: the bot token, who may talk to it, and which alerts it
// sends.

package menu

import (
	"fmt"
	"strings"

	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/telegram"
	"github.com/backpack/backpack/internal/tui"
)

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
		tui.Info(fmt.Sprintf("Admins                : %d", telegram.AdminCount(cfg)))
	} else {
		tui.Info("Not configured yet.")
	}
	fmt.Println()

	idx := tui.ChooseOpt("Choose:", []tui.Option{
		{Title: "Configure / Update bot", Desc: "token, admin id, tunnel relay"},
		{Title: "Alerts", Desc: "warn when CPU, memory, disk or a tunnel goes bad"},
		{Title: "Admins", Desc: "who else may use the bot, and who may only look"},
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
		configureAdmins(cfg)
	case 3:
		diagnoseRelay()
	case 4:
		if err := telegram.SendStatusNow(); err != nil {
			tui.Error("Failed: " + err.Error())
		} else {
			tui.Success("Report sent.")
		}
		tui.PressEnter()
	case 5:
		if err := telegram.Disable(); err != nil {
			tui.Error("Failed: " + err.Error())
		} else {
			tui.Success("Telegram reports disabled.")
		}
		tui.PressEnter()
	}
}

// configureAdmins edits who else may drive the bot.
//
// The owner is not on the editable list and cannot be removed here: locking
// yourself out of the bot from inside the bot's own settings is not a mistake
// worth making possible.
func configureAdmins(cfg telegram.Config) {
	tui.Clear()
	tui.Title("Telegram Admins")
	fmt.Println()

	if cfg.AdminID == "" {
		tui.Error("Configure the bot first — the owner is set there.")
		tui.PressEnter()
		return
	}

	tui.Info("Currently allowed:")
	tui.Info(telegram.AdminsSummary(cfg))
	fmt.Println()
	tui.Info("Enter the extra ids separated by commas. Add \":ro\" to an id to give")
	tui.Info("it every screen but no buttons that change anything.")
	tui.Info("Leave blank to keep the list as it is; type \"none\" to clear it.")
	fmt.Println()

	// Blank means "changed my mind", not "remove everyone": pressing enter at a
	// prompt is how people back out, and it must not delete the admin list.
	answer := tui.Prompt("Extra admins: ")
	switch {
	case answer == "":
		tui.Info("Unchanged.")
		tui.PressEnter()
		return
	case strings.EqualFold(answer, "none"):
		cfg.Admins = nil
	default:
		cfg.Admins = telegram.ParseAdmins(answer)
		if len(cfg.Admins) == 0 {
			tui.Error("None of those look like Telegram ids — nothing was changed.")
			tui.PressEnter()
			return
		}
	}

	if err := telegram.Save(cfg); err != nil {
		tui.Error("Failed to save: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success(fmt.Sprintf("Saved — %d account(s) may use the bot.", telegram.AdminCount(cfg)))
	tui.PressEnter()
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
