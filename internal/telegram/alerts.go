package telegram

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/alerthist"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/sysstat"
)

// Alerting.
//
// A status report every N hours tells you what was true when the cron fired.
// It does not tell you that the processor has been pinned since twenty minutes
// ago, or that a tunnel dropped and came back. This watches continuously and
// speaks only when something changes.
//
// Two rules keep it from becoming noise, which is the failure mode that makes
// people mute a monitoring bot and then miss the outage that mattered:
//
//   - Hysteresis. An alert fires when a reading crosses the threshold and does
//     not fire again until the reading has fallen clearly below it. A value
//     hovering on the line produces one message, not forty.
//   - Cooldown. While a condition persists, it is repeated at most once per
//     cooldown, as a reminder rather than a stream.
//
// Every alert has a matching recovery message, because "CPU is at 94%" is only
// actionable if you also learn when it stopped.

// Alert thresholds and behaviour. Zero for a threshold disables that check.
type AlertConfig struct {
	Enabled bool `json:"enabled"`

	CPUPercent  int `json:"cpu_percent"`
	MemPercent  int `json:"mem_percent"`
	DiskPercent int `json:"disk_percent"`

	// TunnelDown reports a tunnel changing between up and down.
	TunnelDown bool `json:"tunnel_down"`

	// NewRelease announces a newer version published on GitHub, once per
	// version.
	NewRelease bool `json:"new_release"`

	// CheckSeconds is how often the machine is sampled.
	CheckSeconds int `json:"check_seconds"`
	// CooldownMinutes is the minimum gap between repeats of the same alert
	// while the condition persists.
	CooldownMinutes int `json:"cooldown_minutes"`
}

// Defaults chosen to be useful without being chatty: thresholds high enough
// that crossing one is genuinely worth a message.
const (
	defaultCPUPercent      = 85
	defaultMemPercent      = 85
	defaultDiskPercent     = 90
	defaultCheckSeconds    = 60
	defaultCooldownMinutes = 30

	// clearMargin is how far a reading must fall below its threshold before the
	// alert is considered cleared. Without it, a value oscillating around the
	// line would alternate alert and recovery messages indefinitely.
	clearMargin = 5
)

// DefaultAlerts returns the configuration applied when none has been saved.
func DefaultAlerts() AlertConfig {
	return AlertConfig{
		Enabled:         true,
		CPUPercent:      defaultCPUPercent,
		MemPercent:      defaultMemPercent,
		DiskPercent:     defaultDiskPercent,
		TunnelDown:      true,
		NewRelease:      true,
		CheckSeconds:    defaultCheckSeconds,
		CooldownMinutes: defaultCooldownMinutes,
	}
}

// normalise fills in anything missing. A config written by a build that predates
// alerts has no alert section at all, so it arrives here as a zero value; that
// must turn into working defaults rather than a watcher that samples every zero
// seconds and never fires.
func (a AlertConfig) normalise() AlertConfig {
	if a.CheckSeconds <= 0 {
		a.CheckSeconds = defaultCheckSeconds
	}
	if a.CooldownMinutes <= 0 {
		a.CooldownMinutes = defaultCooldownMinutes
	}
	return a
}

// Summary renders the current alert settings for the bot and the CLI.
func (a AlertConfig) Summary() string {
	a = a.normalise()
	if !a.Enabled {
		return "🔕 Alerts are off."
	}
	var b strings.Builder
	b.WriteString("🔔 Alerts are on\n\n")
	b.WriteString(thresholdLine("Processor", a.CPUPercent))
	b.WriteString(thresholdLine("Memory", a.MemPercent))
	b.WriteString(thresholdLine("Disk", a.DiskPercent))
	if a.TunnelDown {
		b.WriteString("• Tunnel up/down: on\n")
	} else {
		b.WriteString("• Tunnel up/down: off\n")
	}
	if a.NewRelease {
		b.WriteString("• New release: on\n")
	} else {
		b.WriteString("• New release: off\n")
	}
	fmt.Fprintf(&b, "\nChecked every %ds, repeated at most every %dm.",
		a.CheckSeconds, a.CooldownMinutes)
	return b.String()
}

func thresholdLine(name string, pct int) string {
	if pct <= 0 {
		return fmt.Sprintf("• %s: off\n", name)
	}
	return fmt.Sprintf("• %s: above %d%%\n", name, pct)
}

// alertState remembers what has already been reported, so a condition that is
// still true does not produce a message on every single check.
type alertState struct {
	firing   bool
	lastSent time.Time
}

// watcher holds the state for one running alert loop.
type watcher struct {
	states map[string]*alertState
	// tunnelUp is the last known up/down state per tunnel. It starts empty, and
	// the first pass only records — otherwise every restart of the panel would
	// announce every tunnel that happens to be down.
	tunnelUp map[string]bool
	seeded   bool
}

func newWatcher() *watcher {
	return &watcher{
		states:   map[string]*alertState{},
		tunnelUp: map[string]bool{},
	}
}

// releaseCheckEvery is how often GitHub is asked for the latest release. A new
// version is not urgent, and from Iran the request may have to fail over
// through the tunnel relay, so this is deliberately infrequent.
const releaseCheckEvery = 6 * time.Hour

// RunAlerts samples the machine and the tunnels until ctx is cancelled, sending
// a message when something crosses a threshold and again when it recovers.
func RunAlerts(ctx context.Context) {
	w := newWatcher()
	for {
		c := Load()
		alerts := c.Alerts.normalise()

		if alerts.NewRelease {
			// In its own goroutine: this is the one part of the loop that makes
			// a network call, and it must not delay a threshold check.
			go manage.RefreshUpdateCheckIfStale(releaseCheckEvery)
		}

		if !alerts.Enabled {
			// Switched off; check back shortly in case it is turned on without
			// restarting the process.
			if !sleepCtx2(ctx, 30*time.Second) {
				return
			}
			continue
		}

		// The checks run whether or not the bot is configured: the web panel
		// reads the recorded history, and it should not need a Telegram token
		// to know the disk filled up. Only the sending needs the bot.
		configured := c.Token != "" && c.AdminID != ""
		if !configured {
			// The release announcement marks itself "already announced" when
			// collected; without a bot to receive it, that would consume the
			// notice silently.
			alerts.NewRelease = false
		}

		tunnels := tunnelStates()
		msgs := w.checkAt(alerts, sysstat.Get(), tunnels, time.Now())
		alerthist.Record(msgs, w.activeConditions(tunnels))
		if configured {
			for _, msg := range msgs {
				send(c, c.AdminID, msg)
			}
		}

		if !sleepCtx2(ctx, time.Duration(alerts.CheckSeconds)*time.Second) {
			return
		}
	}
}

// activeConditions summarises everything currently firing, for the panel's
// alert view. It reads the same state the hysteresis uses, so the panel and
// the bot can never disagree about whether an alert is live.
func (w *watcher) activeConditions(tunnels map[string]bool) []string {
	var out []string
	for _, c := range []struct{ key, label string }{
		{"cpu", "Processor above threshold"},
		{"mem", "Memory above threshold"},
		{"disk", "Disk above threshold"},
	} {
		if st := w.states[c.key]; st != nil && st.firing {
			out = append(out, c.label)
		}
	}
	names := make([]string, 0, len(tunnels))
	for name := range tunnels {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !tunnels[name] {
			out = append(out, "Tunnel "+name+" down")
		}
	}
	return out
}

// checkAt runs one pass with the readings and the clock injected. It is
// separated from the sending and the sleeping so it can be tested directly,
// which is the only way to prove the hysteresis actually holds.
func (w *watcher) checkAt(a AlertConfig, s sysstat.Snapshot, tunnels map[string]bool, now time.Time) []string {
	var msgs []string
	cooldown := time.Duration(a.CooldownMinutes) * time.Minute

	resource := func(key, label string, value float64, threshold int, extra string) {
		if threshold <= 0 {
			return
		}
		st := w.states[key]
		if st == nil {
			st = &alertState{}
			w.states[key] = st
		}
		switch {
		case value >= float64(threshold):
			// Fire on the crossing, then at most once per cooldown after that.
			if !st.firing {
				st.firing, st.lastSent = true, now
				msgs = append(msgs, fmt.Sprintf("⚠️ %s at %.1f%% (threshold %d%%)\n%s",
					label, value, threshold, extra))
			} else if now.Sub(st.lastSent) >= cooldown {
				st.lastSent = now
				msgs = append(msgs, fmt.Sprintf("⚠️ %s still at %.1f%%\n%s", label, value, extra))
			}
		case st.firing && value < float64(threshold-clearMargin):
			// Only clear once well below the line, so a value sitting on the
			// threshold cannot flap between alert and recovery.
			st.firing = false
			msgs = append(msgs, fmt.Sprintf("✅ %s back to normal — %.1f%%", label, value))
		}
	}

	resource("cpu", "Processor", s.CPUPercent, a.CPUPercent,
		fmt.Sprintf("Load: %s · %d cores", s.LoadString(), s.CPUCores))
	resource("mem", "Memory", s.MemPercent, a.MemPercent,
		fmt.Sprintf("%s of %s used", sysstat.HumanBytes(s.MemUsed), sysstat.HumanBytes(s.MemTotal)))
	resource("disk", "Disk", s.DiskPercent, a.DiskPercent,
		fmt.Sprintf("%s of %s used", sysstat.HumanBytes(s.DiskUsed), sysstat.HumanBytes(s.DiskTotal)))

	if a.TunnelDown {
		msgs = append(msgs, w.checkTunnels(tunnels)...)
	}
	if a.NewRelease {
		msgs = append(msgs, releaseMessages()...)
	}
	return msgs
}

// releaseMessages announces a newer release once. The "already announced" mark
// is stored on disk rather than in memory, so restarting the panel does not
// re-announce a version the admin has already been told about.
//
// It reads the cache written by the background check; it never makes the
// network call itself, so a blocked GitHub cannot stall the alert loop.
func releaseMessages() []string {
	tag, ok := manage.UpdateNeedsNotifying()
	if !ok {
		return nil
	}
	manage.MarkUpdateNotified(tag)
	return []string{fmt.Sprintf(
		"⬆️ Backpack %s has been released (you are on %s).\n\n"+
			"Update from the CLI: sudo backpack → Update.\n"+
			"It saves a restore point first and rolls back by itself if the "+
			"tunnel does not come back up.", tag, app.Version)}
}

// checkTunnels reports transitions only. The first pass seeds the state without
// announcing anything, so restarting the panel is not an alert storm.
func (w *watcher) checkTunnels(now map[string]bool) []string {
	if !w.seeded {
		w.tunnelUp = now
		w.seeded = true
		return nil
	}

	var msgs []string
	names := make([]string, 0, len(now))
	for name := range now {
		names = append(names, name)
	}
	sort.Strings(names) // stable message order

	for _, name := range names {
		up := now[name]
		was, known := w.tunnelUp[name]
		switch {
		case !known:
			// A newly created tunnel: record it, but only announce if it is
			// already down, which is worth knowing immediately.
			if !up {
				msgs = append(msgs, fmt.Sprintf("🔴 Tunnel *%s* is down", name))
			}
		case was && !up:
			msgs = append(msgs, fmt.Sprintf("🔴 Tunnel *%s* went down", name))
		case !was && up:
			msgs = append(msgs, fmt.Sprintf("🟢 Tunnel *%s* is back up", name))
		}
	}
	for name := range w.tunnelUp {
		if _, still := now[name]; !still {
			msgs = append(msgs, fmt.Sprintf("🗑 Tunnel *%s* no longer exists", name))
		}
	}

	w.tunnelUp = now
	return msgs
}

// tunnelStates reports whether each configured tunnel is currently up. A tunnel
// counts as up when its peer connection is established, not merely when the
// service is running — a client retrying a filtered address forever has a
// healthy-looking unit and no tunnel.
func tunnelStates() map[string]bool {
	out := map[string]bool{}
	for _, t := range manage.List() {
		h := manage.TunnelHealth(t)
		out[t.Name] = h.Active && h.Connected
	}
	return out
}

// sleepCtx2 waits for d, reporting false if the context was cancelled first.
func sleepCtx2(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
