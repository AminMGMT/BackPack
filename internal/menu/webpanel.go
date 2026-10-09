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
		fmt.Printf("  %sStatus%s      %s● Running%s\n", tui.Gray, tui.Reset, tui.Bold+tui.White, tui.Reset)
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
		fmt.Printf("  %sLogin Code%s  %s%s%s\n", tui.Gray, tui.Reset, tui.Bold+tui.Red, cfg.Password, tui.Reset)
	} else {
		fmt.Printf("  %sStatus%s      %s○ Stopped%s %s(Restart Panel Starts It)%s\n",
			tui.Gray, tui.Reset, tui.Red, tui.Reset, tui.Gray, tui.Reset)
	}
	tui.Rule()
}

// webPanelMenu is main-menu item 5 — the monitoring web UI.
func webPanelMenu() {
	for {
		tui.Clear()
		tui.Title("Web Panel")
		fmt.Println()
		cfg := webui.Load()
		panelHeader(cfg)
		fmt.Println()

		idx := tui.ChooseOpt("Web Panel", []tui.Option{
			{Title: "Panel Port", Desc: fmt.Sprintf("now %d", cfg.Port)},
			{Title: "New Login Code", Desc: "random 8 digits"},
			{Title: "Custom Password", Desc: "instead of the login code"},
			{Title: "Panel Path", Desc: panelPathDesc(cfg)},
			{Title: "Certificate", Desc: panelCertDesc(cfg)},
			{Title: "Two-Factor Sign-In", Desc: twoFactorDesc()},
			{Title: "Restart Panel", Desc: "starts it when stopped"},
			{Title: "Stop Panel", Desc: "disable the web UI"},
		})
		switch idx {
		case 0:
			changePanelPort()
		case 1:
			c, err := webui.RegeneratePassword()
			if err != nil {
				tui.Error("Failed: " + err.Error())
			} else {
				tui.Success("New Login Code: " + c.Password)
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
				tui.Success("Panel Restarted.")
			}
			tui.PressEnter()
		case 7:
			if err := webui.Disable(); err != nil {
				tui.Error("Failed: " + err.Error())
			} else {
				tui.Success("Panel Stopped.")
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
	tui.Title("Panel Path")
	fmt.Println()

	host := cachedServerIP()
	if cfg.TLSDomain != "" {
		host = cfg.TLSDomain
	}
	fmt.Printf("  %sAddress%s  %s%s%s\n\n", tui.Gray, tui.Reset,
		tui.Bold+tui.White, cfg.URL(host), tui.Reset)

	switch tui.ChooseOpt("Panel Path", []tui.Option{
		{Title: "Keep It", Desc: ""},
		{Title: "New Random Path", Desc: "the old address stops working"},
		{Title: "My Own Path", Desc: "letters, digits, - and _"},
		{Title: "No Path", Desc: "found by any scan"},
	}) {
	case 1:
		c, err := webui.RegenerateBasePath()
		if err != nil {
			tui.Error("Failed: " + err.Error())
		} else {
			tui.Success("Panel: " + c.URL(host))
			tui.Warn("The Old Address No Longer Works.")
		}
		tui.PressEnter()
	case 2:
		p := strings.TrimSpace(tui.Prompt("Path: "))
		if p == "" {
			return
		}
		c, err := webui.SetBasePath(p)
		if err != nil {
			tui.Error("Failed: " + err.Error())
		} else {
			tui.Success("Panel: " + c.URL(host))
		}
		tui.PressEnter()
	case 3:
		if !tui.Confirm("Serve Without A Path (Found By Any Scan)", false) {
			return
		}
		c, err := webui.SetBasePath("/")
		if err != nil {
			tui.Error("Failed: " + err.Error())
		} else {
			tui.Success("Panel: " + c.URL(host))
		}
		tui.PressEnter()
	}
}

// panelCertDesc summarises how the panel is reached, for the menu line.
func panelCertDesc(cfg webui.Config) string {
	switch {
	case !cfg.HTTPS:
		return "plain HTTP — no certificate"
	case cfg.OwnCert():
		return "HTTPS with your own certificate (" + cfg.TLSCertFile + ")"
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
	tui.Title("Panel Certificate")
	fmt.Println()
	tui.Info("Now: " + panelCertDesc(cfg))
	fmt.Println()

	idx := tui.ChooseOpt("Serve The Panel Over", []tui.Option{
		{Title: "Plain HTTP", Desc: "default"},
		{Title: "HTTPS Self-Signed", Desc: "works on an IP; the browser warns once"},
		{Title: "HTTPS Let's Encrypt", Desc: "needs a domain and port 80"},
		{Title: "HTTPS My Own Certificate", Desc: "PEM files you have"},
	})

	// Every choice but the last forgets a brought certificate.
	if idx >= 0 && idx < 3 {
		cfg.TLSCertFile, cfg.TLSKeyFile = "", ""
	}

	switch idx {
	case 0:
		cfg.HTTPS, cfg.TLSDomain, cfg.TLSEmail = false, "", ""
	case 1:
		cfg.HTTPS, cfg.TLSDomain, cfg.TLSEmail = true, "", ""
	case 2:
		domain := strings.TrimSpace(tui.PromptDefault("Domain Pointing Here (Port 80 Open)", cfg.TLSDomain))
		if domain == "" {
			tui.Warn("No Domain — Nothing Changed.")
			tui.PressEnter()
			return
		}
		email := strings.TrimSpace(tui.PromptDefault("Email For Expiry Warnings (Optional)", cfg.TLSEmail))
		cfg.HTTPS, cfg.TLSDomain, cfg.TLSEmail = true, domain, email
	case 3:
		certFile := strings.TrimSpace(tui.PromptDefault("Certificate File (fullchain.pem)", cfg.TLSCertFile))
		keyFile := strings.TrimSpace(tui.PromptDefault("Key File (privkey.pem)", cfg.TLSKeyFile))
		names, notAfter, err := webui.CheckOwnCert(certFile, keyFile)
		if err != nil {
			tui.Error(err.Error())
			tui.Warn("Nothing Changed.")
			tui.PressEnter()
			return
		}
		tui.Success(fmt.Sprintf("Certificate For %s, Valid Until %s.",
			strings.Join(names, ", "), notAfter.Format("2006-01-02")))
		cfg.HTTPS, cfg.TLSDomain, cfg.TLSEmail, cfg.TLSSelfHost = true, "", "", ""
		cfg.TLSCertFile, cfg.TLSKeyFile = certFile, keyFile
	default:
		return
	}

	if err := webui.SaveTLS(cfg); err != nil {
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
	tui.Success("Saved: " + panelCertDesc(cfg) + ".")
	host := cachedServerIP()
	if cfg.TLSDomain != "" {
		host = cfg.TLSDomain
	}
	tui.Warn(fmt.Sprintf("New Address: %s", cfg.URL(host)))
	if cfg.HTTPS && cfg.TLSDomain != "" {
		tui.Warn("The First Request Takes A Few Seconds.")
	}
	tui.PressEnter()
}

// changePanelPort moves the web panel to a different port and restarts it.
func changePanelPort() {
	fmt.Println()
	cur := webui.Load().Port
	p := tui.PromptInt("Panel Port", cur)
	if p == cur {
		return
	}
	if p < 1 || p > 65535 {
		tui.Error("Invalid port (1-65535).")
		tui.PressEnter()
		return
	}
	if manage.PortInUse(strconv.Itoa(p)) {
		tui.Error(fmt.Sprintf("Port %d is already in use.", p))
		tui.PressEnter()
		return
	}
	if _, err := webui.SetPort(p); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success(fmt.Sprintf("Panel Port: %d — Restarted.", p))
	tui.PressEnter()
}

// setCustomPassword prompts for a custom web-panel password and applies it.
func setCustomPassword() {
	fmt.Println()
	pw := tui.Prompt("New Password (4-128 Chars): ")
	if len(pw) < 4 || len(pw) > 128 {
		tui.Error("4 to 128 characters.")
		tui.PressEnter()
		return
	}
	confirm := tui.Prompt("Repeat Password: ")
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
	tui.Success("Password Saved.")
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
	tui.Title("Two-Factor Sign-In")
	fmt.Println()

	if !webui.TwoFactorEnabled() {
		tui.Info("Off — Turn It On In The Panel: Settings → Security.")
		tui.PressEnter()
		return
	}

	tui.Info("On — " + twoFactorDesc() + ".")
	fmt.Println()
	tui.Warn("For A Lost Phone And Lost Recovery Codes.")

	if !tui.Confirm("Turn Two-Factor Off", false) {
		return
	}
	if err := webui.DisableTwoFactor(); err != nil {
		tui.Error("Failed: " + err.Error())
	} else {
		tui.Success("Two-Factor Off.")
	}
	tui.PressEnter()
}
