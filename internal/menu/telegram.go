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
		tui.Info(fmt.Sprintf("Configured — Reports Every %d Hour(s).", telegram.IntervalHours()))
		tui.Info("Relay  : " + telegram.RelayStatus())
		tui.Info("Alerts : " + alertSummaryLine(cfg.Alerts))
		tui.Info(fmt.Sprintf("Admins : %d", telegram.AdminCount(cfg)))
	} else {
		tui.Info("Not Configured.")
	}
	fmt.Println()

	idx := tui.ChooseOpt("Telegram Bot", []tui.Option{
		{Title: "Configure Bot", Desc: "token, admin id, relay"},
		{Title: "Alerts", Desc: "CPU, memory, disk, tunnels"},
		{Title: "Admins", Desc: "who may use the bot"},
		{Title: "Diagnose Relay", Desc: "find the broken hop"},
		{Title: "Send Test Report", Desc: ""},
		{Title: "Disable Reports", Desc: ""},
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
			tui.Success("Report Sent.")
		}
		tui.PressEnter()
	case 5:
		if err := telegram.Disable(); err != nil {
			tui.Error("Failed: " + err.Error())
		} else {
			tui.Success("Reports Disabled.")
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

	tui.Info("Allowed:")
	tui.Info(telegram.AdminsSummary(cfg))
	fmt.Println()
	tui.Info("Ids, Comma Separated (id:ro = Read Only). Blank = Keep, none = Clear.")
	fmt.Println()

	// Blank means "changed my mind", not "remove everyone": pressing enter at a
	// prompt is how people back out, and it must not delete the admin list.
	answer := tui.Prompt("Extra Admins: ")
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
			tui.Error("No valid Telegram ids — nothing changed.")
			tui.PressEnter()
			return
		}
	}

	if err := telegram.Save(cfg); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success(fmt.Sprintf("Saved — %d Account(s).", telegram.AdminCount(cfg)))
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
	tui.Warn("Alert When Crossed And When Recovered. 0 = Off.")
	fmt.Println()

	if cfg.Token == "" {
		tui.Error("Configure the bot first — there is nowhere to send an alert.")
		tui.PressEnter()
		return
	}

	a := cfg.Alerts
	a.Enabled = tui.Confirm("Send Alerts", a.Enabled)
	if a.Enabled {
		a.CPUPercent = tui.PromptInt("CPU %", a.CPUPercent)
		a.MemPercent = tui.PromptInt("Memory %", a.MemPercent)
		a.DiskPercent = tui.PromptInt("Disk %", a.DiskPercent)
		a.TunnelDown = tui.Confirm("Tunnel Down / Back", a.TunnelDown)
		a.NewRelease = tui.Confirm("New Backpack Release", a.NewRelease)
		a.CheckSeconds = tui.PromptInt("Check Every (Seconds)", a.CheckSeconds)
		a.CooldownMinutes = tui.PromptInt("Repeat Every (Minutes)", a.CooldownMinutes)
	}

	cfg.Alerts = a
	if err := telegram.Save(cfg); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Alerts Saved.")
	fmt.Println()
	tui.Info(a.Summary())
	fmt.Println()

	tui.PressEnter()
}

// configureTelegram sets up the bot. On an Iran server Telegram is blocked, so
// the primary path relays traffic through a tunnel: backpack forwards a
// loopback port on the chosen tunnel straight to api.telegram.org and sends
// every bot request through it, with the peer making the outbound connection.
func configureTelegram(cfg telegram.Config) {
	tui.Info("Token From @BotFather, Your Id From @userinfobot.")
	fmt.Println()
	cfg.Token = tui.PromptDefault("Bot Token", cfg.Token)
	cfg.AdminID = tui.PromptDefault("Admin Id", cfg.AdminID)

	if cfg.Token == "" || cfg.AdminID == "" {
		tui.Error("Token and admin id are required.")
		tui.PressEnter()
		return
	}

	fmt.Println()
	tunnels := manage.List()
	if len(tunnels) == 0 {
		tui.Warn("No Tunnels Yet — On Iran The Bot Needs A Tunnel Relay.")
		if !tui.Confirm("Send Directly (Only Where Telegram Is Reachable)", false) {
			return
		}
		cfg.ViaTunnel = ""
	} else {
		// Automatic first, and the default. Pinning a tunnel means the bot goes
		// silent exactly when that tunnel drops — which is the moment its
		// warnings matter most.
		opts := []tui.Option{{
			Title: "Automatic (Recommended)",
			Desc:  "any connected tunnel",
		}}
		for _, t := range tunnels {
			opts = append(opts, tui.Option{
				Title: "Always " + t.Name,
				Desc:  fmt.Sprintf("%s %s — pinned; the bot goes quiet if it drops", t.Role, t.Transport),
			})
		}
		opts = append(opts, tui.Option{
			Title: "Direct",
			Desc:  "only where Telegram is reachable",
		})

		idx := tui.ChooseOpt("Send Through", opts)
		switch {
		case idx < 0:
			return

		case idx == 0:
			cfg.ViaTunnel = telegram.AutoRelay
			cfg.SocksPort = 0 // resolved per request
			tui.Info("Preparing A Relay...")
			if name, port, err := telegram.PrepareAutoRelay(); err != nil {
				tui.Warn("Not Ready Yet: " + err.Error() + " — It Keeps Trying.")
			} else {
				tui.Success(fmt.Sprintf("Relay Ready On %s (Port %d).", name, port))
				tui.Warn("Restart That Tunnel's Kharej Side Once.")
			}

		case idx <= len(tunnels):
			cfg.ViaTunnel = tunnels[idx-1].Name
			tui.Info("Setting Up A Relay Through " + cfg.ViaTunnel + "...")
			port, err := manage.EnsureSocksPort(cfg.ViaTunnel)
			if err != nil {
				tui.Error("Relay failed: " + err.Error())
				tui.PressEnter()
				return
			}
			cfg.SocksPort = port
			tui.Success(fmt.Sprintf("Relay Ready (Port %d).", port))
			tui.Warn("Restart The Kharej Side Once.")

		default:
			cfg.ViaTunnel = ""
		}
	}

	fmt.Println()
	cfg.IntervalHours = tui.PromptInt("Report Every (Hours)", maxInt(cfg.IntervalHours, 6))
	if err := telegram.Configure(cfg); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	if err := telegram.SendTest(cfg); err != nil {
		tui.Warn("Saved, But The Test Failed: " + err.Error())
	} else {
		tui.Success("Saved — Test Delivered.")
	}
	tui.PressEnter()
}
