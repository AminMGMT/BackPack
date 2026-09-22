// The Web Panel screen: the port, the password, the secret base path and the
// certificate.

package menu

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/webui"
)

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
		// The whole address, path included. The panel is served under an
		// unguessable segment, so this screen is where an operator finds it —
		// it is on the machine they already have a shell on, which is the one
		// place it can be read without being findable by anybody else.
		fmt.Printf("  %sWeb Panel%s   %s%s%s\n", tui.Gray, tui.Reset,
			tui.Bold+tui.White, cfg.URL(host), tui.Reset)
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
			{Title: "Panel path", Desc: panelPathDesc(cfg)},
			{Title: "Certificate", Desc: panelCertDesc(cfg)},
			{Title: "Two-factor sign-in", Desc: twoFactorDesc()},
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
			panelPathMenu(cfg)
		case 4:
			panelCertMenu(cfg)
		case 5:
			twoFactorMenu()
		case 6:
			if _, err := webui.EnsureRunning(); err != nil {
				tui.Error("Failed: " + err.Error())
			} else if err := manage.RestartService(app.WebUIService); err != nil {
				tui.Error("Failed: " + err.Error())
			} else {
				tui.Success("Web panel restarted.")
			}
			tui.PressEnter()
		case 7:
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

// panelPathDesc is the menu line for the panel's path.
func panelPathDesc(cfg webui.Config) string {
	if p := cfg.PathPrefix(); p != "" {
		return "served under " + p + "/"
	}
	return "served at the root — anyone scanning the port finds it"
}

// panelPathMenu shows the path the panel is served under and lets it be moved.
//
// The path is what a port sweep hits instead of a login page. It is not
// authentication and rotating it on a schedule buys nothing — what this is for
// is the day it stops being unguessable, because it was pasted into a chat or
// left on a screenshot. Then the old one is worth throwing away, and this is
// how, without editing JSON on a server.
func panelPathMenu(cfg webui.Config) {
	tui.Clear()
	tui.Title("Panel path")
	tui.Info("The panel answers under this path and nowhere else. Every other " +
		"address on this port is a 404 that says nothing about a panel being here.")
	fmt.Println()

	host := cachedServerIP()
	if cfg.TLSDomain != "" {
		host = cfg.TLSDomain
	}
	fmt.Printf("  %sAddress%s  %s%s%s\n\n", tui.Gray, tui.Reset,
		tui.Bold+tui.White, cfg.URL(host), tui.Reset)

	switch tui.ChooseOpt("Choose:", []tui.Option{
		{Title: "Keep it", Desc: "nothing changes"},
		{Title: "Generate a new one", Desc: "the current address stops working"},
		{Title: "Set my own", Desc: "letters, digits, - and _"},
		{Title: "Serve at the root", Desc: "no path — the panel is found by any scan"},
	}) {
	case 1:
		c, err := webui.RegenerateBasePath()
		if err != nil {
			tui.Error("Failed: " + err.Error())
		} else {
			tui.Success("The panel is now at " + c.URL(host))
			tui.Warn("The old address no longer works. Write this one down.")
		}
		tui.PressEnter()
	case 2:
		p := strings.TrimSpace(tui.Prompt("Path segment: "))
		if p == "" {
			return
		}
		c, err := webui.SetBasePath(p)
		if err != nil {
			tui.Error("Failed: " + err.Error())
		} else {
			tui.Success("The panel is now at " + c.URL(host))
		}
		tui.PressEnter()
	case 3:
		if !tui.Confirm("Serve the panel at the root, where any scan of this port finds it?", false) {
			return
		}
		c, err := webui.SetBasePath("/")
		if err != nil {
			tui.Error("Failed: " + err.Error())
		} else {
			tui.Success("The panel is now at " + c.URL(host))
		}
		tui.PressEnter()
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
	tui.Warn(fmt.Sprintf("The address changed — use %s", cfg.URL(host)))
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

// twoFactorDesc is the line under the menu entry.
func twoFactorDesc() string {
	if !webui.TwoFactorEnabled() {
		return "off — the panel password is the whole login"
	}
	left := webui.RecoveryCodesLeft()
	return fmt.Sprintf("on · %d recovery %s left", left, map[bool]string{true: "code", false: "codes"}[left == 1])
}

// twoFactorMenu is the way back in.
//
// Turning the second factor *on* is a job for the panel: it needs to show a
// secret to scan and a list of recovery codes to keep, and a terminal is the
// wrong place for both. Turning it off is the opposite — it is what somebody
// does when they cannot reach the panel, from the one place that proves they
// own the machine.
func twoFactorMenu() {
	tui.Clear()
	tui.Title("Two-factor sign-in")
	fmt.Println()

	if !webui.TwoFactorEnabled() {
		tui.Info("Two-factor is off. The panel password is the whole login.")
		fmt.Println()
		tui.Warn("Turn it on from the panel itself — Settings → Security. It has to")
		tui.Warn("show you a secret to scan and a set of recovery codes to keep, and")
		tui.Warn("a terminal is the wrong place to read either of them from.")
		tui.PressEnter()
		return
	}

	tui.Info("Two-factor is on. " + twoFactorDesc() + ".")
	fmt.Println()
	tui.Warn("This screen exists for one situation: the phone is gone and so are")
	tui.Warn("the recovery codes. Turning it off here needs no password, because")
	tui.Warn("anyone who can run this can already read the file the secret is in.")
	fmt.Println()

	if !tui.Confirm("Turn two-factor off", false) {
		return
	}
	if err := webui.DisableTwoFactor(); err != nil {
		tui.Error("Failed: " + err.Error())
	} else {
		tui.Success("Two-factor is off. The panel password is the whole login again.")
	}
	tui.PressEnter()
}
