package telegram

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/geo"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/sysstat"
	"github.com/backpack/backpack/internal/tunhist"
)

// Driving the tunnels from the chat.
//
// Callback data is capped at 64 bytes, which a tunnel name plus an action verb
// can exceed and, worse, can exceed only for the one person whose tunnel is
// named after the city it lives in. So a button carries a hash of the name
// rather than the name. It is a pure function of the name, so it survives a
// restart and needs nothing stored — unlike an index into the list, which would
// silently point at a different tunnel the moment one was added or deleted.

// tunnelID is the short, stable handle a button carries.
func tunnelID(name string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return fmt.Sprintf("%08x", h.Sum32())
}

// tunnelByID resolves a handle back to a tunnel, or reports that it is gone —
// which is what a button from before a tunnel was deleted now means.
func tunnelByID(id string) (manage.Tunnel, bool) {
	for _, t := range manage.List() {
		if tunnelID(t.Name) == id {
			return t, true
		}
	}
	return manage.Tunnel{}, false
}

// --- the tunnel list --------------------------------------------------------

// tunnelsPerRow keeps a name and its status dot readable on a phone. Four fits
// only if every tunnel is called "de1".
const tunnelsPerRow = 3

// tunnelsScreen lists every tunnel as a button, coloured by its live state.
func tunnelsScreen(lang string) reply {
	tunnels := manage.List()
	if len(tunnels) == 0 {
		return screenReply(
			b("🎛 "+tr(lang, "Tunnels"))+"\n\n"+esc(tr(lang, "No tunnels configured.")),
			kb(backTo(lang, "nav:home")))
	}

	health := manage.AllHealth()
	var rows [][]btn
	var current []btn
	for _, t := range tunnels {
		current = append(current, btn{
			Text: stateIcon(health[t.Name].State) + " " + t.Name,
			Data: "t:" + tunnelID(t.Name),
		})
		if len(current) == tunnelsPerRow {
			rows = append(rows, current)
			current = nil
		}
	}
	if len(current) > 0 {
		rows = append(rows, current)
	}

	rows = append(rows,
		row(btn{Text: "📊 " + tr(lang, "OVERVIEW"), Data: "nav:overview"}),
		row(btn{Text: "🔄 " + tr(lang, "Restart all"), Data: "act:restartall"},
			btn{Text: "🔃 " + tr(lang, "Refresh"), Data: "nav:tunnels"}),
		backTo(lang, "nav:home"),
	)

	text := b("🎛 "+tr(lang, "Tunnels")) + "\n\n" +
		esc(tr(lang, "Select your tunnel to manage.")) + "\n\n" +
		esc(tunnelTally(lang, tunnels, health))

	return screenReply(text, kb(rows...))
}

// tunnelTally is the one line worth reading before the buttons: how many are
// up, and how many are not.
func tunnelTally(lang string, tunnels []manage.Tunnel, health map[string]manage.Health) string {
	var online, offline, stopped int
	for _, t := range tunnels {
		switch health[t.Name].State {
		case "online":
			online++
		case "offline":
			offline++
		default:
			stopped++
		}
	}
	parts := []string{fmt.Sprintf("🟢 %d", online)}
	if offline > 0 {
		parts = append(parts, fmt.Sprintf("🟡 %d", offline))
	}
	if stopped > 0 {
		parts = append(parts, fmt.Sprintf("🔴 %d", stopped))
	}
	return strings.Join(parts, "   ") + "   ·   " +
		fmt.Sprintf(tr(lang, "%d total"), len(tunnels))
}

// --- one tunnel -------------------------------------------------------------

// tunnelRoute handles everything under a single tunnel: the detail screen and
// the read-only views hanging off it.
func tunnelRoute(lang, rest string) reply {
	id, view, _ := strings.Cut(rest, ":")
	t, ok := tunnelByID(id)
	if !ok {
		return goneReply(lang)
	}
	switch view {
	case "logs":
		return logsScreen(lang, t)
	case "traffic":
		return trafficScreen(lang, t)
	case "ping":
		return pingScreen(lang, t)
	}
	return tunnelScreen(lang, t)
}

// goneReply is what a button pointing at a deleted tunnel produces.
func goneReply(lang string) reply {
	r := tunnelsScreen(lang)
	r.toast = tr(lang, "That tunnel no longer exists.")
	r.alert = true
	return r
}

// tunnelScreen is the detail view: everything worth knowing about one tunnel,
// and the three buttons that drive it.
func tunnelScreen(lang string, t manage.Tunnel) reply {
	h := manage.TunnelHealth(t)
	var out strings.Builder

	fmt.Fprintf(&out, "%s ", stateIcon(h.State))
	if f := tunnelFlag(t); f != "" {
		fmt.Fprintf(&out, "%s ", f)
	}
	fmt.Fprintf(&out, "%s [ %s ]", b(t.Name), esc(strings.ToUpper(t.Transport)))
	if p := manage.PresetLabel(t.Name); p != "" {
		fmt.Fprintf(&out, " [ %s ]", esc(p))
	}
	out.WriteString("\n\n")

	// Two independent questions, which a reverse tunnel lets you answer with
	// one test and a direct tunnel does not: where the tunnel's own socket is,
	// and whether this side exposes the forwarded ports. In a direct tunnel
	// the Iran side does both dial out and hold the ports.
	if manage.DialsOut(t) {
		fmt.Fprintf(&out, tr(lang, "Server")+" : %s\n", code(t.Addr))
	} else {
		fmt.Fprintf(&out, tr(lang, "Tunnel Port")+" : %s\n", code(portOf(t.Addr)))
	}
	if manage.HoldsPorts(t) {
		if ports := manage.VisiblePorts(t.Ports, manage.TunnelToken(t.Name)); len(ports) > 0 {
			fmt.Fprintf(&out, tr(lang, "Forwarded Port")+" : %s\n", code(strings.Join(ports, ", ")))
		}
	}

	if snap, err := metrics.Read(app.ConfigDir, t.Name); err == nil {
		fmt.Fprintf(&out, "↑ %s | ↓ %s | Σ %s\n",
			sysstat.HumanBytes(snap.BytesOut),
			sysstat.HumanBytes(snap.BytesIn),
			sysstat.HumanBytes(snap.BytesIn+snap.BytesOut))
	}
	if up, ok := uptime24h(t.Name); ok {
		fmt.Fprintf(&out, tr(lang, "Uptime 24h")+" : %.1f%%\n", up)
	}
	fmt.Fprintf(&out, tr(lang, "Peer")+" : %s\n", esc(peerDescription(lang, t, h)))

	// The state line only earns its place when it says something the dot does
	// not — "running, but no client is connected yet" is the whole diagnosis for
	// the most confusing state a tunnel has.
	if h.State != "online" && h.Detail != "" {
		fmt.Fprintf(&out, "\n%s %s", stateIcon(h.State), esc(tr(lang, h.Detail)))
	}

	id := tunnelID(t.Name)
	return screenReply(out.String(), kb(
		row(btn{Text: "📄 " + tr(lang, "Logs"), Data: "t:" + id + ":logs"}),
		row(btn{Text: "▶️ " + tr(lang, "Start"), Data: "act:start:" + id},
			btn{Text: "⏹ " + tr(lang, "Stop"), Data: "act:stop:" + id},
			btn{Text: "🔄 " + tr(lang, "Restart"), Data: "act:restart:" + id}),
		row(btn{Text: "📈 " + tr(lang, "Traffic"), Data: "t:" + id + ":traffic"},
			btn{Text: "📡 " + tr(lang, "Ping"), Data: "t:" + id + ":ping"}),
		refreshAndBack(lang, "t:"+id, "nav:tunnels"),
	))
}

// peerDescription says who is on the far end and where they are, which is the
// line that turns "connected" into something you can act on: a peer in the
// wrong country means the wrong tunnel is carrying the traffic.
func peerDescription(lang string, t manage.Tunnel, h manage.Health) string {
	if !h.Connected {
		return tr(lang, "not connected")
	}
	ip := peerIP(t)
	if ip == "" {
		return tr(lang, "connected")
	}
	g := geo.Lookup(ip)
	if g == nil {
		return ip
	}
	var parts []string
	switch {
	case g.City != "" && g.Country != "":
		parts = append(parts, g.City+", "+g.Country)
	case g.Country != "":
		parts = append(parts, g.Country)
	}
	if g.ISP != "" {
		parts = append(parts, g.ISP)
	}
	if len(parts) == 0 {
		return ip
	}
	return strings.Join(parts, " · ")
}

// uptime24h reports the share of the last day the tunnel was actually up,
// reading the history the monitor samples. The second return is false when
// nothing has been sampled yet, so a fresh install shows no line rather than a
// confident 0%.
func uptime24h(name string) (float64, bool) {
	h := tunhist.Load().Tunnels[name]
	if h == nil {
		return 0, false
	}
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	var up, total int
	for _, b := range h.Hourly {
		if b.T < cutoff {
			continue
		}
		up += b.UpN
		total += b.N
	}
	if total == 0 {
		// Less than an hour of history: the five-minute samples are all there is.
		for _, s := range h.Recent {
			if s.T < cutoff {
				continue
			}
			total++
			if s.Up {
				up++
			}
		}
	}
	if total == 0 {
		return 0, false
	}
	return float64(up) / float64(total) * 100, true
}

// --- logs -------------------------------------------------------------------

// logLines is how much of the journal fits in a message with room for the
// keyboard. clampText enforces the real limit; this keeps the request small.
const logLines = 40

func logsScreen(lang string, t manage.Tunnel) reply {
	out := strings.TrimSpace(manage.Logs(t.Name, logLines))
	if out == "" {
		out = tr(lang, "No log output.")
	}
	id := tunnelID(t.Name)
	text := b("📄 "+tr(lang, "Logs")+" — "+t.Name) + "\n\n" + preBlock(out)
	return screenReply(text, kb(refreshAndBack(lang, "t:"+id+":logs", "t:"+id)))
}

// --- traffic ----------------------------------------------------------------

// sparkChars is the eight-level ramp the chart is drawn with. A real chart
// would be an image; this is legible in a notification and costs nothing.
var sparkChars = []rune("▁▂▃▄▅▆▇█")

func trafficScreen(lang string, t manage.Tunnel) reply {
	id := tunnelID(t.Name)
	hist := tunhist.Load().Tunnels[t.Name]
	title := b("📈 " + tr(lang, "Traffic") + " — " + t.Name)

	if hist == nil || len(hist.Hourly) < 2 {
		return screenReply(
			title+"\n\n"+esc(tr(lang, "Not enough history yet — the sampler needs about an hour.")),
			kb(refreshAndBack(lang, "t:"+id+":traffic", "t:"+id)))
	}

	deltas, peak, total := hourlyDeltas(hist.Hourly, 24)
	var out strings.Builder
	out.WriteString(title + "\n\n")
	out.WriteString(esc(tr(lang, "Last 24 hours")) + "\n")
	out.WriteString("<code>" + sparkline(deltas, peak) + "</code>\n\n")
	fmt.Fprintf(&out, tr(lang, "Busiest hour")+" : %s\n", sysstat.HumanBytes(peak))
	fmt.Fprintf(&out, tr(lang, "Total")+" : %s\n", sysstat.HumanBytes(total))
	if up, ok := uptime24h(t.Name); ok {
		fmt.Fprintf(&out, tr(lang, "Uptime 24h")+" : %.1f%%\n", up)
	}

	return screenReply(out.String(), kb(refreshAndBack(lang, "t:"+id+":traffic", "t:"+id)))
}

// hourlyDeltas turns cumulative counters into per-hour traffic.
//
// The counters restart from zero whenever the tunnel's process does, so a
// bucket lower than the one before it is a restart, not negative traffic. The
// bytes since the restart are the honest reading for that hour; treating it as
// zero would hide the traffic, and subtracting would produce a number the size
// of the whole previous total.
func hourlyDeltas(buckets []tunhist.Hour, want int) (deltas []uint64, peak, total uint64) {
	// One extra bucket at the front: the first delta needs something to
	// subtract from.
	from := len(buckets) - want - 1
	if from < 0 {
		from = 0
	}
	window := buckets[from:]

	for i := 1; i < len(window); i++ {
		prev := window[i-1].In + window[i-1].Out
		cur := window[i].In + window[i].Out
		var d uint64
		if cur >= prev {
			d = cur - prev
		} else {
			d = cur // counters were reset
		}
		deltas = append(deltas, d)
		total += d
		if d > peak {
			peak = d
		}
	}
	return deltas, peak, total
}

// sparkline draws values as blocks scaled to the peak. An all-zero series draws
// as a flat floor rather than an empty string, because "no traffic" is an
// answer and a blank line looks like a bug.
func sparkline(values []uint64, peak uint64) string {
	if len(values) == 0 {
		return strings.Repeat(string(sparkChars[0]), 1)
	}
	var out strings.Builder
	for _, v := range values {
		if peak == 0 {
			out.WriteRune(sparkChars[0])
			continue
		}
		idx := int(v * uint64(len(sparkChars)-1) / peak)
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkChars) {
			idx = len(sparkChars) - 1
		}
		out.WriteRune(sparkChars[idx])
	}
	return out.String()
}

// --- ping -------------------------------------------------------------------

// pingScreen measures the path to the far end.
//
// TCP rather than ICMP, for the reason ProbePath documents: the routes these
// tunnels run over frequently drop ping while carrying traffic perfectly well.
//
// Twelve probes with a four-second timeout is up to a minute on a dead link —
// far longer than Telegram will hold a button press open, and a minute in which
// the bot would answer nothing else. So the screen draws first and measures
// afterwards.
func pingScreen(lang string, t manage.Tunnel) reply {
	id := tunnelID(t.Name)
	target := probeTarget(t)

	if target == "" {
		return screenReply(
			b("📡 "+tr(lang, "Ping")+" — "+t.Name)+"\n\n"+
				esc(tr(lang, "No peer address to measure — the tunnel is not connected.")),
			kb(refreshAndBack(lang, "t:"+id+":ping", "t:"+id)))
	}

	r := measuring(lang, "📡 "+tr(lang, "Ping")+" — "+t.Name, "t:"+id+":ping", "t:"+id)
	r.after = func(c Config, chat string, messageID int64) {
		go pushResult(c, chat, messageID, pingResult(lang, t, target))
	}
	return r
}

// pingResult renders a finished measurement.
func pingResult(lang string, t manage.Tunnel, target string) reply {
	id := tunnelID(t.Name)
	title := b("📡 " + tr(lang, "Ping") + " — " + t.Name)
	footer := kb(refreshAndBack(lang, "t:"+id+":ping", "t:"+id))

	q := manage.ProbePath(target)
	var out strings.Builder
	out.WriteString(title + "\n\n")
	fmt.Fprintf(&out, tr(lang, "Target")+" : %s\n\n", code(target))
	if !q.Usable() {
		detail := tr(lang, "no reply")
		if q.Err != nil {
			detail = q.Err.Error()
		}
		out.WriteString("🔴 " + esc(detail))
		return screenReply(out.String(), footer)
	}
	fmt.Fprintf(&out, tr(lang, "Latency")+" : %d ms\n", q.Avg.Milliseconds())
	fmt.Fprintf(&out, tr(lang, "Jitter")+" : %d ms\n", q.Jitter.Milliseconds())
	fmt.Fprintf(&out, tr(lang, "Loss")+" : %.0f%%\n", q.LossPercent())
	fmt.Fprintf(&out, "min %d ms · max %d ms\n", q.Min.Milliseconds(), q.Max.Milliseconds())

	return screenReply(out.String(), footer)
}

// probeTarget is the address worth measuring: the server a client dials, or the
// peer currently connected to a server.
func probeTarget(t manage.Tunnel) string {
	if t.Role == "client" {
		return t.Addr
	}
	snap, err := metrics.Read(app.ConfigDir, t.Name)
	if err != nil || snap.Peer == "" {
		return ""
	}
	return snap.Peer
}
