package manage

import (
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// The Connection Test's table, drawn while the test runs.
//
// Each tunnel is one row, and a row changes as its echoes come back: STATUS
// reads TESTING, ECHOES counts the echoes sent so far, and the bar fills with
// them — progress = sent / total, from the test itself, not an animation. Only
// rows that changed are rewritten, in place, so the screen does not flicker
// with seventeen tunnels reporting at once. When the test ends the table is
// set out once more as the verdict: the rows grouped OK, UNSTABLE, DOWN, each
// bar filled by the echoes that came back, and under it the tunnels that
// carried everything, with their round trip and speed.

// ctBarSegments is how many segments a bar has.
const ctBarSegments = 8

// ctTransportNames are the names the menus use.
var ctTransportNames = map[string]string{
	"tcp": "TCP", "tcpmux": "TCP MUX", "stealth": "Stealth", "pck": "PCK",
	"ws": "WS", "wss": "WSS", "wsmux": "WS MUX", "wssmux": "WSS MUX",
	"kcp": "KCP FEC", "quic": "QUIC", "udp": "UDP", "xdi": "xDi", "sni": "SNI",
	"naive": "Naive / HTTP2", "xhttp": "XHTTP / TLS", "reality": "REALITY / Vision",
	ctSpoofToKharej: "Iran→Kharej", ctSpoofToIran: "Kharej→Iran",
}

func ctName(tr string) string {
	if n, ok := ctTransportNames[tr]; ok {
		return n
	}
	return strings.ToUpper(tr)
}

func ctKind(kind string) string {
	if kind == "" {
		return ""
	}
	return strings.ToUpper(kind[:1]) + kind[1:]
}

// ctBar is a bar filled to share (0–1), with the percentage beside it.
func ctBar(share float64) string {
	share = math.Max(0, math.Min(1, share))
	filled := int(math.Round(share * ctBarSegments))
	return fmt.Sprintf("%s%s %3d%%", strings.Repeat("▰", filled),
		strings.Repeat("▱", ctBarSegments-filled), int(math.Round(share*100)))
}

func ctEmoji(status string) string {
	switch status {
	case ctOK:
		return "🟢"
	case ctDown:
		return "🔴"
	case ctSkipped:
		return "⚪"
	default: // testing, unstable
		return "🟡"
	}
}

// Every emoji here takes two columns; the rest of the RESULT column is spaces.
const ctRowFormat = "%s       %-9s %-16s %-11s %-8s %s"

const ctHeader = "RESULT   KIND      NAME        STATUS      ECHOES   TESTING"

// ctRow is one tunnel's line. While it runs, ECHOES and the bar are the echoes
// sent; once it has a verdict, the echoes that came back.
func ctRow(r ConnTestResult) string {
	total := r.Total
	if total <= 0 {
		total = connTestSoak
	}
	done := r.OK
	if r.Status == ctTesting {
		done = r.Tried
	}
	return fmt.Sprintf(ctRowFormat, ctEmoji(r.Status), ctKind(r.Kind), ctName(r.Transport),
		strings.ToUpper(r.Status), fmt.Sprintf("%d/%d", done, total), ctBar(float64(done)/float64(total)))
}

func ctRule() string { return strings.Repeat("─", 60) }

// ConnTestTable is the verdict as text: the grouped table and the tunnels
// that carried everything.
func ConnTestTable(results []ConnTestResult) string {
	var b strings.Builder
	b.WriteString(ctHeader + "\n")
	for _, group := range ctGroups(results) {
		b.WriteString("\n")
		for _, r := range group {
			b.WriteString(ctRow(r) + "\n")
		}
	}
	b.WriteString("\n" + ctRule() + "\n\nWorks Steadily:\n\n")
	steady := 0
	for _, group := range ctGroups(results) {
		for _, r := range group {
			if r.Status != ctOK {
				continue
			}
			steady++
			rtt, speed := "-", "-"
			// The spoofing probes carry no reply, so they have no round trip.
			if r.RTTms > 0 {
				rtt = fmt.Sprintf("%dms", r.RTTms)
			} else if r.OK > 0 && r.Kind != "spoof" {
				rtt = "<1ms"
			}
			if r.Mbps > 0 {
				speed = fmt.Sprintf("%.1f Mbps", r.Mbps)
			}
			fmt.Fprintf(&b, "%s %-19s %-7s %s\n", ctEmoji(ctOK), ctKind(r.Kind)+" "+ctName(r.Transport), rtt, speed)
		}
	}
	if steady == 0 {
		b.WriteString("Nothing carried traffic steadily between these two servers. See the\n" +
			"statuses and notes for setup errors, interruption or path failures.\n")
	}
	// Why, where the table alone cannot say: a spoofing check that failed, and
	// anything that was not run.
	var notes []string
	for _, r := range results {
		if r.Detail != "" && r.Status != ctOK {
			notes = append(notes, fmt.Sprintf("%s %s %s: %s", ctEmoji(r.Status), ctKind(r.Kind), ctName(r.Transport), r.Detail))
		}
	}
	if len(notes) > 0 {
		b.WriteString("\nNotes:\n\n" + strings.Join(notes, "\n") + "\n")
	}
	return b.String()
}

// ctGroups orders the verdict as the table shows it: what worked, fastest
// first; what dropped, most echoes first; then what never came up, reverse and
// direct apart; then what was not run.
func ctGroups(results []ConnTestResult) [][]ConnTestResult {
	var ok, unstable, downReverse, downDirect, downSpoof, skipped []ConnTestResult
	for _, r := range results {
		switch r.Status {
		case ctOK:
			ok = append(ok, r)
		case ctDown:
			switch r.Kind {
			case "direct":
				downDirect = append(downDirect, r)
			case "spoof":
				downSpoof = append(downSpoof, r)
			default:
				downReverse = append(downReverse, r)
			}
		case ctSkipped:
			skipped = append(skipped, r)
		default:
			unstable = append(unstable, r)
		}
	}
	sort.SliceStable(ok, func(i, j int) bool { return ok[i].Mbps > ok[j].Mbps })
	sort.SliceStable(unstable, func(i, j int) bool { return unstable[i].OK > unstable[j].OK })
	var out [][]ConnTestResult
	for _, g := range [][]ConnTestResult{ok, unstable, downReverse, downDirect, downSpoof, skipped} {
		if len(g) > 0 {
			out = append(out, g)
		}
	}
	return out
}

// ctBoard draws the live table and keeps it current.
type ctBoard struct {
	out   io.Writer
	title string
	tty   bool

	mu    sync.Mutex
	rows  []ConnTestResult
	shown []string // what each line of the drawn region reads now
	stop  chan struct{}
	done  chan struct{}
}

// newCTBoard starts a board under title. On a terminal it redraws changed
// rows in place a few times a second; anywhere else it prints only the
// verdict.
func newCTBoard(out io.Writer, title string) *ctBoard {
	b := &ctBoard{out: out, title: title, tty: ctIsTerminal(out),
		stop: make(chan struct{}), done: make(chan struct{})}
	go b.loop()
	return b
}

func ctIsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// set records one tunnel's state.
func (b *ctBoard) set(i int, r ConnTestResult) {
	b.mu.Lock()
	for len(b.rows) <= i {
		b.rows = append(b.rows, ConnTestResult{})
	}
	b.rows[i] = r
	b.mu.Unlock()
}

// setAll replaces every row, for the kharej, which is sent them together.
func (b *ctBoard) setAll(rows []ConnTestResult) {
	b.mu.Lock()
	b.rows = append([]ConnTestResult(nil), rows...)
	b.mu.Unlock()
}

func (b *ctBoard) loop() {
	defer close(b.done)
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-t.C:
			if b.tty {
				b.draw()
			}
		}
	}
}

// lines is the live region: title, rule, header, one line per tunnel, rule.
func (b *ctBoard) lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.rows) == 0 {
		return nil
	}
	out := []string{"CONNECTION TEST — " + b.title, ctRule(), "", ctHeader, ""}
	for _, r := range b.rows {
		out = append(out, ctRow(r))
	}
	return append(out, "", ctRule())
}

// draw rewrites the lines that changed since the last draw, and nothing else.
func (b *ctBoard) draw() {
	lines := b.lines()
	if len(lines) == 0 {
		return
	}
	var w strings.Builder
	if len(b.shown) != len(lines) {
		// First draw, or the row count changed: the region is written out.
		if n := len(b.shown); n > 0 {
			fmt.Fprintf(&w, "\033[%dF\033[J", n)
		}
		for _, l := range lines {
			w.WriteString(l + "\n")
		}
	} else {
		// The cursor sits under the region; each changed line is reached from
		// there, rewritten, and left.
		for i, l := range lines {
			if l == b.shown[i] {
				continue
			}
			up := len(lines) - i
			fmt.Fprintf(&w, "\033[%dF\033[2K%s\033[%dE", up, l, up)
		}
	}
	b.shown = lines
	if w.Len() > 0 {
		_, _ = io.WriteString(b.out, w.String())
	}
}

// finish stops the live drawing and sets the verdict out in its place, and
// the recommendation under it.
func (b *ctBoard) finish(results []ConnTestResult, best ConnTestBest) {
	close(b.stop)
	<-b.done
	var w strings.Builder
	if b.tty && len(b.shown) > 0 {
		fmt.Fprintf(&w, "\033[%dF\033[J", len(b.shown))
	}
	w.WriteString("CONNECTION TEST — " + b.title + "\n" + ctRule() + "\n\n")
	w.WriteString(ConnTestTable(results))
	w.WriteString("\n" + ConnTestBestTable(best))
	_, _ = io.WriteString(b.out, w.String())
}

// abandon stops the live drawing and leaves what is on the screen.
func (b *ctBoard) abandon() {
	close(b.stop)
	<-b.done
}
