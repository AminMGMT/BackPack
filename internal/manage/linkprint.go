package manage

import (
	"fmt"
	mrand "math/rand/v2"
	"net"
	"strings"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/tui"
)

// How the other side is built from a setup link, as the Iran wizard, its
// summary and Manage → Setup Link all print it: the link itself, for a kharej
// that already runs Backpack, and one command that installs Backpack and
// applies the link, for a kharej that does not.

// InstallCommand is the one line that installs Backpack on a server and builds
// the tunnel in link there. It is run as root on the kharej; install.sh hands
// what follows it to the installed binary (see the end of install.sh).
func InstallCommand(link string) string {
	// The published release may predate managed HTTPS link support. Installing
	// it here would replace the matching test build and lose the optional helpers.
	if l, err := DecodeShareLink(link); err == nil && managedTransport(l.Tr) {
		return ""
	}
	return fmt.Sprintf("bash <(curl -fsSL https://raw.githubusercontent.com/%s/%s/main/install.sh) link apply '%s'",
		app.RepoOwner, app.RepoName, link)
}

// printLinkBlock prints the two ways to build the kharej from link. menuPath
// is where the Setup Link answer is in the kharej's menu, for the first.
func printLinkBlock(link, menuPath string) {
	command := InstallCommand(link)
	if command == "" {
		menuPath = "sudo backpack → Setup Kharej → Reverse → HTTPS → Same Protocol → Setup Link"
	}
	fmt.Println()
	tui.Info("Setup Link (" + menuPath + "):")
	fmt.Println(tui.Color(tui.Bold+tui.White, link))
	fmt.Println()
	if command == "" {
		tui.Info("Install The Same BackPack Build On Kharej With BP_HELPERS=naive,xray, Then Apply:")
		fmt.Println(tui.Color(tui.Bold+tui.White, "sudo backpack link apply '"+link+"'"))
	} else {
		tui.Info("Install BackPack And Set Up This Tunnel (Kharej Without BackPack, As Root):")
		fmt.Println(tui.Color(tui.Bold+tui.White, command))
	}
}

// askLinkBackupHosts asks for this server's other addresses, which the setup
// link hands to the kharej as backups to the main one. Each is an IP or a
// domain — the tunnel port is used — or host:port.
func askLinkBackupHosts() []string {
	raw := strings.TrimSpace(tui.PromptDefault(
		"Backup Addresses For The Kharej (Other IPs Or Domains Of This Server, Comma Separated, Optional)", ""))
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if h, p, err := net.SplitHostPort(part); err == nil {
			if h == "" || !validPort(p) {
				tui.Warn("Skipped " + part + ": Not An Address With A Port.")
				continue
			}
		} else if strings.ContainsAny(part, " /") {
			tui.Warn("Skipped " + part + ": Not An IP Or Domain.")
			continue
		}
		out = append(out, part)
	}
	return out
}

// askScheduledRestart asks whether both servers should restart this tunnel
// together every so many hours, and at which minute past the hour it happens:
// one chosen at random, so tunnels set up together do not all restart at once.
func askScheduledRestart() (hours, minute int) {
	hours = EffectiveRestartHours(tui.PromptInt("Restart This Tunnel On Both Servers Every N Hours (0 = Off)", 0))
	if hours <= 0 {
		return 0, 0
	}
	return hours, mrand.IntN(60)
}

// scheduleLabel is a restart schedule in words.
func scheduleLabel(hours, minute int) string {
	if hours <= 0 {
		return "off"
	}
	return fmt.Sprintf("every %d hours, at :%02d UTC, on both servers", hours, minute)
}

// applyLinkExtras keeps, for a tunnel just created on the Iran side, what its
// link carried beyond the config: the backup addresses, so Manage → Setup Link
// hands them out again, and the restart schedule, on this server too.
func applyLinkExtras(name string, x linkExtras) {
	if len(x.hosts) > 0 {
		SetLinkHosts(name, x.hosts)
	}
	if x.restartHours > 0 {
		if err := SetScheduledRestart(name, x.restartHours, x.restartMinute); err != nil {
			tui.Warn("Scheduled Restart Not Set: " + err.Error())
		} else {
			tui.Info("Scheduled Restart: " + scheduleLabel(x.restartHours, x.restartMinute) + ".")
		}
	}
}

// scheduleFromLink sets, on the side a link built, the restart schedule the
// other side keeps, so the two restart together.
func scheduleFromLink(name string, link ShareLink) {
	if link.RestartHours <= 0 {
		return
	}
	if err := SetScheduledRestart(name, link.RestartHours, link.RestartMinute); err != nil {
		tui.Warn("Scheduled Restart Not Set: " + err.Error())
		return
	}
	tui.Info("Scheduled Restart: " + scheduleLabel(EffectiveRestartHours(link.RestartHours), link.RestartMinute) + ".")
}
