package webui

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/manage"
)

// The GRE carrier in the Add form.
//
// A kernel GRE link has no tunnel port and no token, and names the other
// server on both sides. The form hides by carrier with data-when, so what is
// pinned here is that the rows a GRE tunnel cannot use are marked "!gre" — and
// so are left out of what is posted, since a hidden row is never sent — that
// the kharej side has a row for the Iran server's address, and that the screen
// says plainly that GRE is not encrypted.
func TestTheAddFormFitsAGRETunnel(t *testing.T) {
	loadPanel()
	raw, err := fs.ReadFile(panelRoot, "views/add.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	direct := html[strings.Index(html, `class="step3direct"`):]

	rowFor := func(name string) []string {
		re := regexp.MustCompile(`<div class="f3[^"]*"(?: data-when="([^"]*)")?><label>[^<]*(?:<span[^>]*>[^<]*</span>)?</label>(?:<div class="withb">)?<input name="` + name + `"`)
		var whens []string
		for _, m := range re.FindAllStringSubmatch(direct, -1) {
			whens = append(whens, m[1])
		}
		return whens
	}

	if got := rowFor("tunnelPort"); len(got) != 1 || got[0] != "!gre" {
		t.Errorf("the direct tunnel port row is shown on GRE: data-when %q", got)
	}
	tokens := rowFor("token")
	if len(tokens) != 2 {
		t.Errorf("found %d direct token rows, want the Iran one and the kharej one", len(tokens))
	}
	for _, when := range tokens {
		if !strings.HasSuffix(when, "-!gre") {
			t.Errorf("a direct token row is shown on GRE: data-when %q", when)
		}
	}
	peer := rowFor("peerAddr")
	hasIran, hasKharej := false, false
	for _, when := range peer {
		hasIran = hasIran || when == "server"
		hasKharej = hasKharej || when == "client-gre"
	}
	if !hasIran || !hasKharej {
		t.Errorf("peerAddr rows %q: want one for Iran and one for a GRE kharej", peer)
	}
	for _, label := range []string{"Error correction (FEC)", "Stealth"} {
		// "!gre-!pck": neither GRE nor PCK has them (see TestTheAddFormFitsAPckTunnel).
		if !regexp.MustCompile(`data-when="!gre[^"]*"><div class="tx"><b>` + regexp.QuoteMeta(label)).MatchString(direct) {
			t.Errorf("%s is offered on GRE, which cannot have it", label)
		}
	}
	if !strings.Contains(direct, `data-when="gre"`) || !strings.Contains(direct, "GRE is not encrypted") {
		t.Error("the form does not say that GRE is not encrypted")
	}

	// The negation has to be something the form's own code evaluates.
	js, err := fs.ReadFile(panelRoot, "js/views/add.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), "part.startsWith('!')") {
		t.Error(`add.js does not evaluate a "!carrier" condition, so the !gre rows would never hide`)
	}
}

// The carriers the form paints come from the server, GRE among them, marked as
// the kernel's own so the form can tell it from the others.
func TestThePanelOffersGRE(t *testing.T) {
	for _, c := range manage.PanelDirectCarriers() {
		if c["value"] == "gre" {
			if c["kernel"] == "" || !strings.Contains(c["desc"], "NOT encrypted") {
				t.Errorf("GRE is offered without saying what it is: %v", c)
			}
			return
		}
	}
	t.Fatal("the panel does not offer GRE")
}

// Found driving the form in a browser: with Direct and GRE chosen, the Tunnel
// step still opened on "Reverse — the kharej server dials in to Iran", and the
// "Carrier tuning" heading stood over nothing, both its switches being !gre.
// add.js now writes the line from the direction and hides a group whose every
// row is hidden.
func TestTheAddFormSaysDirectAndDropsEmptyGroups(t *testing.T) {
	loadPanel()
	raw, err := fs.ReadFile(panelRoot, "views/add.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	if !strings.Contains(html, `id="dirnote"`) {
		t.Fatal("the direction line #dirnote is gone from add.html")
	}
	tuning := html[strings.Index(html, "Carrier tuning"):]
	tuning = tuning[:strings.Index(tuning, `<div class="grp3">`)]
	if n := strings.Count(tuning, `data-when="!gre`); n == 0 || strings.Count(tuning, `class="tg3`) != n {
		t.Fatalf("Carrier tuning is no longer all !gre rows (%d of them); recheck what GRE shows there", n)
	}
	js, err := fs.ReadFile(panelRoot, "js/views/add.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, want := range []string{
		`querySelector('#dirnote')`,
		`'Direct — this server dials out`,
		`rows.every(r => r.hidden) ? 'none' : ''`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("add.js lacks %q", want)
		}
	}
}

// Found deleting a GRE tunnel from its card: the confirmation listed
// /etc/backpack/<name>.json as the file to be removed. A tunnel's config is
// <name>.toml (app.ConfigPath); the .json beside it is its metrics snapshot.
func TestTheDeleteConfirmationNamesTheConfigFile(t *testing.T) {
	loadPanel()
	js, err := fs.ReadFile(panelRoot, "js/views/dashboard.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	if !strings.Contains(src, "`/etc/backpack/${name}.toml`") || strings.Contains(src, "`/etc/backpack/${name}.json`") {
		t.Error("the delete confirmation does not name the tunnel's .toml config")
	}
}

// Found reading a finished Connection test in a browser: a GRE tunnel's
// five-digit speeds ("↓ 34441 max 34441 Mb/s") did not fit the 118px figures
// column and ran left over the echo count. The column is 152px now.
func TestTheConnectionTestRowFitsFiveDigitSpeeds(t *testing.T) {
	loadPanel()
	css, err := fs.ReadFile(panelRoot, "css/screens/tools.css")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`\.panel \.ct-row\{display:grid;grid-template-columns:10px minmax\(80px,1fr\) minmax\(60px,1fr\) 44px (\d+)px 62px;`).FindSubmatch(css)
	if m == nil {
		t.Fatal("the .ct-row grid is not where this test expects it")
	}
	if w, _ := strconv.Atoi(string(m[1])); w < 150 {
		t.Errorf("the figures column is %dpx; five-digit speeds need about 150", w)
	}
}

func TestTheGRECardSaysGREInsteadOfADashForItsPort(t *testing.T) {
	loadPanel()
	js, err := fs.ReadFile(panelRoot, "js/views/dashboard.js")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`\(ip6\)\?gre`).Match(js) || !strings.Contains(string(js), `'GRE'`) {
		t.Error("a kernel-GRE card has no tunnel port and must say GRE, not \"port —\"")
	}
}

func TestTheTrendBadgeDropsItsNumberOnATinyBaseline(t *testing.T) {
	loadPanel()
	js, err := fs.ReadFile(panelRoot, "js/views/dashboard.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), "TREND_FLOOR") || strings.Count(string(js), "trendText(st)") < 2 {
		t.Error("both trend renderers must go through trendText, which applies the baseline floor")
	}
}

func TestTheConnectionTestNameWrapsAndCarriesATitle(t *testing.T) {
	loadPanel()
	css, _ := fs.ReadFile(panelRoot, "css/screens/tools.css")
	js, _ := fs.ReadFile(panelRoot, "js/views/conntest.js")
	if !regexp.MustCompile(`\.ct-row \.nm b\{[^}]*line-clamp:2`).Match(css) {
		t.Error("the transport name should wrap to two lines, not cut at one")
	}
	if !strings.Contains(string(js), "nb.title = r.name") {
		t.Error("the full transport name belongs in a title attribute")
	}
}
