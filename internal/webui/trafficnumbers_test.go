package webui

import (
	"encoding/json"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/sysstat"
)

// The report these exist for arrived four times over: the per-second rates in
// the server strip read 0, the overview showed no all-time traffic and no
// per-tunnel totals, the tunnel cards showed none either, and the metrics
// screen's traffic rows were blank. One cause underneath all four.
//
// This API sends every size and speed twice: once formatted for a reader
// ("200.0 MiB", "873 B/s") and once as a number. The classic panel prints the
// formatted one. The new panel has to compute — it scales a column history
// against its peak, works out each tunnel's share of the busiest, animates the
// headline figure up to its value — and Number("200.0 MiB") is NaN, which
// every one of those turned into a silent zero. A page reporting no traffic on
// a link carrying hundreds of megabytes.
//
// Parsing the formatted string back in the browser is not the fix: it is
// guesswork about units that goes wrong the first time the formatter changes.
// The number travels alongside, and these guard both halves of that.

// formatted names the fields that are prose, and must only ever be printed.
var formatted = []string{
	"totalSent", "totalRecv", "totalTraffic", "upSpeed", "downSpeed",
	"bytesIn", "bytesOut", "bytesTotal",
}

// numeric names their siblings, which are what anything computing must read.
var numeric = []string{
	"totalSentBytes", "totalRecvBytes", "totalTrafficBytes", "upBps", "downBps",
	"inBytes", "outBytes", "totalBytes",
}

func TestEveryFormattedFigureHasANumberBesideIt(t *testing.T) {
	var sys SystemStats
	fillNetwork(&sys, nil)
	var tun TunnelInfo
	fillMetrics(&tun, metrics.Snapshot{BytesIn: 209764000, BytesOut: 81300000})

	keys := map[string]bool{}
	for _, v := range []any{sys, tun} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for k := range m {
			keys[k] = true
		}
	}
	for _, want := range numeric {
		if !keys[want] {
			t.Errorf("%q is not in the response; the panel has nothing to compute with "+
				"and will read the formatted string as a number, which is NaN", want)
		}
	}
	for _, want := range formatted {
		if !keys[want] {
			t.Errorf("%q is gone from the response; the classic panel prints it", want)
		}
	}
}

// The two forms have to describe the same traffic, or the cards and the figure
// above them disagree and there is no way to tell which is right. Formatting
// the number has to give back the string that travelled with it.
func TestTheNumberAndTheStringAgree(t *testing.T) {
	snap := metrics.Snapshot{Name: "nl-ws", BytesIn: 209764000, BytesOut: 81300000}
	var tun TunnelInfo
	fillMetrics(&tun, snap)

	if tun.InBytes != snap.BytesIn || tun.OutBytes != snap.BytesOut {
		t.Errorf("the tunnel's numbers do not match its snapshot: got %d/%d, want %d/%d",
			tun.InBytes, tun.OutBytes, snap.BytesIn, snap.BytesOut)
	}
	if tun.TotalBytes != snap.BytesIn+snap.BytesOut {
		t.Errorf("totalBytes is %d, not in+out (%d)", tun.TotalBytes, snap.BytesIn+snap.BytesOut)
	}
	for _, c := range []struct {
		what   string
		num    uint64
		formed string
	}{
		{"bytesIn", tun.InBytes, tun.BytesIn},
		{"bytesOut", tun.OutBytes, tun.BytesOut},
		{"bytesTotal", tun.TotalBytes, tun.BytesTotal},
	} {
		if got := sysstat.HumanBytes(c.num); got != c.formed {
			t.Errorf("%s reads %q but its number formats to %q — the card and the "+
				"figure above it would disagree", c.what, c.formed, got)
		}
	}

	// The system totals are summed from the same snapshots, so the same
	// identity has to hold there. With no tunnels both halves are zero, which
	// still catches one of the pair being left unset.
	var sys SystemStats
	fillNetwork(&sys, nil)
	if got := sysstat.HumanBytes(sys.TotalTrafficBytes); got != sys.TotalTraffic {
		t.Errorf("totalTraffic reads %q but totalTrafficBytes formats to %q",
			sys.TotalTraffic, got)
	}
	if sys.TotalTrafficBytes != sys.TotalSentBytes+sys.TotalRecvBytes {
		t.Errorf("totalTrafficBytes (%d) is not sent plus received (%d)",
			sys.TotalTrafficBytes, sys.TotalSentBytes+sys.TotalRecvBytes)
	}
}

// And the panel must not go back to computing with the prose.
func TestThePanelNeverComputesWithAFormattedFigure(t *testing.T) {
	loadExperimentalPanel()

	sources := map[string]string{}
	for _, rel := range []string{
		"js/ui/strip.js", "js/views/overview.js", "js/views/dashboard.js", "js/views/metrics.js",
	} {
		b, err := fs.ReadFile(panelRoot, rel)
		if err != nil {
			t.Fatalf("cannot read %s: %v", rel, err)
		}
		sources[rel] = string(b)
	}

	for _, f := range formatted {
		// Passed to a formatter, which parses it as a number.
		into := regexp.MustCompile(`\b(bytes|speed)\([^)]*\.` + f + `\b`)
		// Or used in arithmetic, where a string is either NaN or, for +, a
		// concatenation of two sizes. The `|| 0` guard that usually sits with
		// it is part of the shape, and is exactly what made this look safe.
		maths := regexp.MustCompile(`\.` + f + `\b\s*(\|\|\s*0)?\s*\)?\s*[-*/]`)
		for rel, src := range sources {
			if m := into.FindString(src); m != "" {
				t.Errorf("%s: %s — %q is formatted for reading; use its numeric sibling, "+
					"or this silently reads as zero", rel, m, f)
			}
			if m := maths.FindString(src); m != "" {
				t.Errorf("%s: %s — arithmetic on %q, which is a string", rel, strings.TrimSpace(m), f)
			}
		}
	}

	// The numeric fields are only useful if something reads them.
	for rel, want := range map[string][]string{
		"js/ui/strip.js":        {"upBps", "downBps"},
		"js/views/overview.js":  {"totalSentBytes", "totalRecvBytes", "totalTrafficBytes", "totalBytes", "inBytes", "outBytes"},
		"js/views/dashboard.js": {"inBytes", "outBytes"},
		"js/views/metrics.js":   {"inBytes", "outBytes"},
	} {
		for _, w := range want {
			if !strings.Contains(sources[rel], w) {
				t.Errorf("%s no longer reads %q, so whatever it draws from it is a parsed string", rel, w)
			}
		}
	}
}
