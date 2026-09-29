package manage

import (
	"bytes"
	"strings"
	"testing"
)

func TestTheBarIsFilledByTheRealShare(t *testing.T) {
	for _, c := range []struct {
		share float64
		want  string
	}{
		{0, "▱▱▱▱▱▱▱▱   0%"},
		{30.0 / 60, "▰▰▰▰▱▱▱▱  50%"},
		{22.0 / 60, "▰▰▰▱▱▱▱▱  37%"},
		{45.0 / 60, "▰▰▰▰▰▰▱▱  75%"},
		{1, "▰▰▰▰▰▰▰▰ 100%"},
	} {
		if got := ctBar(c.share); got != c.want {
			t.Errorf("share %.2f: %q, want %q", c.share, got, c.want)
		}
	}
}

// While a tunnel runs, its row counts the echoes sent — progress; once it has
// a verdict, the echoes that came back.
func TestARowShowsProgressWhileTestingAndTheVerdictAfter(t *testing.T) {
	running := ConnTestResult{Kind: "reverse", Transport: "stealth", Status: ctTesting, Tried: 30, OK: 20, Total: 60}
	if row := ctRow(running); !strings.Contains(row, "TESTING") || !strings.Contains(row, "30/60") ||
		!strings.Contains(row, "▰▰▰▰▱▱▱▱  50%") || !strings.HasPrefix(row, "🟡") {
		t.Errorf("running row: %q", row)
	}
	done := ConnTestResult{Kind: "reverse", Transport: "stealth", Status: ctUnstable, Tried: 60, OK: 52, Total: 60}
	if row := ctRow(done); !strings.Contains(row, "UNSTABLE") || !strings.Contains(row, "52/60") ||
		!strings.Contains(row, "87%") || !strings.Contains(row, "Stealth") {
		t.Errorf("unstable row: %q", row)
	}
	down := ConnTestResult{Kind: "direct", Transport: "sni", Status: ctDown, Total: 60}
	if row := ctRow(down); !strings.HasPrefix(row, "🔴") || !strings.Contains(row, "0/60") ||
		!strings.Contains(row, "▱▱▱▱▱▱▱▱   0%") || !strings.Contains(row, "Direct") {
		t.Errorf("down row: %q", row)
	}
	ok := ConnTestResult{Kind: "direct", Transport: "xdi", Status: ctOK, Tried: 60, OK: 60, Total: 60}
	if row := ctRow(ok); !strings.HasPrefix(row, "🟢") || !strings.Contains(row, "▰▰▰▰▰▰▰▰ 100%") || !strings.Contains(row, "xDi") {
		t.Errorf("ok row: %q", row)
	}
}

func TestTheVerdictIsGroupedAndListsWhatWorks(t *testing.T) {
	table := ConnTestTable([]ConnTestResult{
		{Kind: "reverse", Transport: "kcp", Status: ctDown, Total: 60},
		{Kind: "direct", Transport: "udp", Status: ctOK, OK: 60, Tried: 60, Total: 60, RTTms: 73, Mbps: 28},
		{Kind: "reverse", Transport: "tcp", Status: ctUnstable, OK: 29, Tried: 60, Total: 60},
		{Kind: "direct", Transport: "pck", Status: ctOK, OK: 60, Tried: 60, Total: 60, RTTms: 69, Mbps: 29.9},
		{Kind: "direct", Transport: "quic", Status: ctDown, Total: 60},
	})
	order := []string{"Direct    PCK", "Direct    UDP", "Reverse   TCP", "Reverse   KCP FEC", "Direct    QUIC", "Works Steadily:",
		"🟢 Direct PCK          69ms    29.9 Mbps", "🟢 Direct UDP          73ms    28.0 Mbps"}
	at := 0
	for _, want := range order {
		i := strings.Index(table[at:], want)
		if i < 0 {
			t.Fatalf("%q missing or out of order in\n%s", want, table)
		}
		at += i + len(want)
	}
}

// Only the rows that changed are written again.
func TestTheLiveTableRewritesOnlyWhatChanged(t *testing.T) {
	var out bytes.Buffer
	b := &ctBoard{out: &out, title: "1.2.3.4", tty: true}
	b.set(0, ConnTestResult{Kind: "reverse", Transport: "tcp", Status: ctTesting, Total: 60})
	b.set(1, ConnTestResult{Kind: "direct", Transport: "pck", Status: ctTesting, Total: 60})
	b.draw()
	if !strings.Contains(out.String(), "CONNECTION TEST — 1.2.3.4") {
		t.Fatalf("first draw: %q", out.String())
	}
	out.Reset()
	b.draw()
	if out.Len() != 0 {
		t.Errorf("nothing changed, yet %q was written", out.String())
	}
	b.set(1, ConnTestResult{Kind: "direct", Transport: "pck", Status: ctTesting, Tried: 1, OK: 1, Total: 60})
	b.draw()
	if n := strings.Count(out.String(), "\033[2K"); n != 1 || !strings.Contains(out.String(), "1/60") {
		t.Errorf("one row changed; %d lines rewritten: %q", n, out.String())
	}
}
