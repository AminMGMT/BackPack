package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The alert settings, as a screen you can change rather than one you can read.
//
// /alerts used to print the thresholds and stop there; changing any of them
// meant an SSH session and the CLI menu. The values worth changing are few and
// each has an obvious ladder of sensible settings, so every one of them is a
// button that steps to the next rung — no keyboard input, which a bot handles
// badly anyway.

// thresholdLadder is the cycle a threshold button steps through. Zero is off,
// and it sits at the end so it takes a deliberate walk to switch a check off
// rather than one stray tap.
var thresholdLadder = []int{70, 75, 80, 85, 90, 95, 0}

// nextThreshold returns the rung after the current value, snapping anything
// unrecognised onto the start of the ladder.
func nextThreshold(current int) int {
	for i, v := range thresholdLadder {
		if v == current {
			return thresholdLadder[(i+1)%len(thresholdLadder)]
		}
	}
	return thresholdLadder[0]
}

// alertsScreen renders the settings with a button for each.
func alertsScreen(c Config, lang string) reply {
	a := c.Alerts.normalise()

	var out strings.Builder
	if a.Enabled {
		out.WriteString(b("🔔 "+tr(lang, "Alerts are on")) + "\n\n")
	} else {
		out.WriteString(b("🔕 "+tr(lang, "Alerts are off")) + "\n\n")
		out.WriteString(esc(tr(lang, "Nothing will be reported until they are switched back on.")) + "\n\n")
	}

	fmt.Fprintf(&out, "• %s : %s\n", esc(tr(lang, "Processor")), thresholdText(lang, a.CPUPercent))
	fmt.Fprintf(&out, "• %s : %s\n", esc(tr(lang, "Memory")), thresholdText(lang, a.MemPercent))
	fmt.Fprintf(&out, "• %s : %s\n", esc(tr(lang, "Disk")), thresholdText(lang, a.DiskPercent))
	fmt.Fprintf(&out, "• %s : %s\n", esc(tr(lang, "Tunnel up/down")), onOff(lang, a.TunnelDown))
	fmt.Fprintf(&out, "• %s : %s\n", esc(tr(lang, "New release")), onOff(lang, a.NewRelease))
	fmt.Fprintf(&out, "\n"+esc(tr(lang, "Checked every %ds, repeated at most every %dm."))+"\n",
		a.CheckSeconds, a.CooldownMinutes)

	power := btn{Text: "🔕 " + tr(lang, "Turn off"), Data: "alert:enabled"}
	if !a.Enabled {
		power = btn{Text: "🔔 " + tr(lang, "Turn on"), Data: "alert:enabled"}
	}

	rows := [][]btn{row(power)}
	if a.Enabled {
		rows = append(rows,
			row(btn{Text: "🧠 CPU " + shortThreshold(a.CPUPercent), Data: "alert:cpu"},
				btn{Text: "💾 RAM " + shortThreshold(a.MemPercent), Data: "alert:mem"},
				btn{Text: "🗄 " + tr(lang, "Disk") + " " + shortThreshold(a.DiskPercent), Data: "alert:disk"}),
			row(btn{Text: "🔗 " + tr(lang, "Tunnel") + " " + tick(a.TunnelDown), Data: "alert:tunnel"},
				btn{Text: "⬆️ " + tr(lang, "Release") + " " + tick(a.NewRelease), Data: "alert:release"}),
		)
	}
	rows = append(rows, backTo(lang, "nav:home"))

	return screenReply(out.String(), kb(rows...))
}

func thresholdText(lang string, pct int) string {
	if pct <= 0 {
		return tr(lang, "off")
	}
	return fmt.Sprintf(tr(lang, "above %d%%"), pct)
}

// shortThreshold is the button label: a percentage, or a dash when the check is
// off. Short, because three of these share a row.
func shortThreshold(pct int) string {
	if pct <= 0 {
		return "—"
	}
	return strconv.Itoa(pct) + "%"
}

func tick(on bool) string {
	if on {
		return "✅"
	}
	return "❌"
}

func onOff(lang string, on bool) string {
	if on {
		return tr(lang, "on")
	}
	return tr(lang, "off")
}

// alertToggle applies one change and redraws.
//
// The config is re-read from disk rather than taken from the caller's copy: the
// alert watcher reloads it every cycle and the web panel writes it too, so the
// copy this press arrived with may be a minute old — and saving it back would
// undo whatever changed in between.
func alertToggle(c Config, u tgUser, lang, field string) reply {
	if !c.canWrite(strconv.FormatInt(u.ID, 10)) {
		r := alertsScreen(c, lang)
		r.toast = tr(lang, "Your access is read-only.")
		r.alert = true
		return r
	}

	cur := Load()
	a := cur.Alerts.normalise()
	switch field {
	case "enabled":
		a.Enabled = !a.Enabled
	case "cpu":
		a.CPUPercent = nextThreshold(a.CPUPercent)
	case "mem":
		a.MemPercent = nextThreshold(a.MemPercent)
	case "disk":
		a.DiskPercent = nextThreshold(a.DiskPercent)
	case "tunnel":
		a.TunnelDown = !a.TunnelDown
	case "release":
		a.NewRelease = !a.NewRelease
	default:
		return alertsScreen(cur, lang)
	}
	cur.Alerts = a

	if err := Save(cur); err != nil {
		r := alertsScreen(c, lang)
		r.toast = "⚠️ " + err.Error()
		r.alert = true
		return r
	}
	recordAudit(AuditEntry{
		Time: time.Now(), User: cur.adminLabel(u), Action: "alerts", Target: field, OK: true,
	})

	r := alertsScreen(cur, lang)
	r.toast = tr(lang, "Saved.")
	return r
}
