package webui

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/manage"
)

// How long a tunnel has been up, as systemd knows it.
//
// The card used to show the metrics snapshot's own figure, which is the age of
// the engine's collector. The engine makes a fresh collector for every
// generation — a reload of its file, a restart asked for over its control
// socket — so the figure fell back to zero whenever something touched the
// tunnel, and what an operator read after signing in was "since some moment"
// rather than "since it started". The service's ActiveEnterTimestamp is the
// start they mean, and it survives everything short of the service itself
// stopping.

// sinceTTL is how long one answer is reused. Uptime moves in minutes on the
// card; the poll that asks is every six seconds.
const sinceTTL = 15 * time.Second

var unitSince = struct {
	mu   sync.Mutex
	at   time.Time
	key  string
	have map[string]time.Time
}{}

// showUnits asks systemd; a variable so a test can answer instead.
var showUnits = manage.Systemctl

// serviceSince returns when each of these units last became active. A unit
// missing from the answer is one systemd has no start time for — stopped, or
// never started — and the caller keeps whatever it had.
func serviceSince(units []string) map[string]time.Time {
	if len(units) == 0 {
		return nil
	}
	key := strings.Join(units, "\x00")
	unitSince.mu.Lock()
	defer unitSince.mu.Unlock()
	if unitSince.key == key && time.Since(unitSince.at) < sinceTTL {
		return unitSince.have
	}
	args := append([]string{"show", "--property=Id,ActiveEnterTimestamp", "--timestamp=unix"}, units...)
	out, err := showUnits(args...)
	if err != nil {
		// systemd before 248 has no --timestamp; it prints the local form,
		// which parseActiveSince also reads.
		out, err = showUnits(append([]string{"show", "--property=Id,ActiveEnterTimestamp"}, units...)...)
	}
	have := map[string]time.Time{}
	if err == nil {
		have = parseActiveSince(out)
	}
	unitSince.at, unitSince.key, unitSince.have = time.Now(), key, have
	return have
}

// parseActiveSince reads `systemctl show` output: blocks of Key=Value lines,
// one block per unit.
func parseActiveSince(out string) map[string]time.Time {
	have := map[string]time.Time{}
	var id string
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "Id":
			id = v
		case "ActiveEnterTimestamp":
			if t, ok := parseSystemdTime(v); ok && id != "" {
				have[id] = t
			}
		}
	}
	return have
}

func parseSystemdTime(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" || v == "n/a" {
		return time.Time{}, false
	}
	if strings.HasPrefix(v, "@") {
		secs, err := strconv.ParseInt(v[1:], 10, 64)
		if err != nil || secs <= 0 {
			return time.Time{}, false
		}
		return time.Unix(secs, 0), true
	}
	// A zone with no abbreviation — Tehran's is one — is printed as an offset.
	for _, layout := range []string{"Mon 2006-01-02 15:04:05 MST", "Mon 2006-01-02 15:04:05 -0700"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// upFor is an uptime the way a card reads it: the two largest units.
func upFor(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
