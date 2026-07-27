package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The panel is deliberately single-themed: one accent, matching the CLI menu.
// These tests guard that decision, because it is the kind of thing a later edit
// re-adds without meaning to — a colour picker looks like a feature, and the
// reason it is absent lives in the changelog rather than the code.

// TestNoThemePicker checks that nothing survives of the removed accent picker.
// A leftover handler is worse than useless: openSettings() called a function
// that no longer existed, which throws and stops the rest of the dialog from
// being set up.
func TestNoThemePicker(t *testing.T) {
	pages := map[string][]byte{
		"dashboard.html": dashboardHTML,
		"login.html":     loginHTML,
	}
	// bp_accent is exempted for login.html, which clears the stale key.
	banned := []string{"applyTheme", "renderSwatches", "THEMES", "swatches"}

	for name, page := range pages {
		body := string(page)
		for _, token := range banned {
			if strings.Contains(body, token) {
				t.Errorf("%s still references the removed theme picker (%q)", name, token)
			}
		}
	}

	// The dashboard must not read a saved accent back at all.
	if strings.Contains(string(dashboardHTML), "getItem('bp_accent')") {
		t.Error("dashboard.html still restores a saved accent, so an old choice would stick")
	}
	// The login page may only remove it, never apply it.
	if strings.Contains(string(loginHTML), "getItem('bp_accent')") {
		t.Error("login.html still applies a saved accent")
	}
	if !strings.Contains(string(loginHTML), "removeItem('bp_accent')") {
		t.Error("login.html should clear the accent saved by older builds")
	}
}

// TestSingleAccent checks that both pages declare the same one accent, so the
// login screen and the dashboard cannot drift apart.
func TestSingleAccent(t *testing.T) {
	re := regexp.MustCompile(`--accent-rgb:\s*([0-9]+,[0-9]+,[0-9]+)`)

	find := func(t *testing.T, name string, page []byte) []string {
		t.Helper()
		m := re.FindAllStringSubmatch(string(page), -1)
		if len(m) == 0 {
			t.Fatalf("%s declares no --accent-rgb at all", name)
		}
		out := make([]string, 0, len(m))
		for _, g := range m {
			out = append(out, g[1])
		}
		return out
	}

	dash := find(t, "dashboard.html", dashboardHTML)
	login := find(t, "login.html", loginHTML)

	if len(dash) != 1 {
		t.Errorf("dashboard.html declares %d accents, want exactly 1: %v", len(dash), dash)
	}
	if len(login) != 1 {
		t.Errorf("login.html declares %d accents, want exactly 1: %v", len(login), login)
	}
	if dash[0] != login[0] {
		t.Errorf("the two pages disagree on the accent: dashboard %q, login %q", dash[0], login[0])
	}
}

// TestGreenMeansOnline is the point of the whole change: green is a status, not
// decoration. If a gauge or a button turns green again, "green = the tunnel is
// up" stops being readable at a glance.
func TestGreenMeansOnline(t *testing.T) {
	body := string(dashboardHTML)

	// The gauges must not carry a status colour.
	ring := regexp.MustCompile(`function ringColor\([^)]*\)\s*\{[^}]*\}`).FindString(body)
	if ring == "" {
		t.Fatal("ringColor() not found — the gauge colour logic moved, so this test needs updating")
	}
	if strings.Contains(ring, "--green") || strings.Contains(ring, "--amber") {
		t.Errorf("the CPU/RAM/disk gauges use a status colour again: %s", ring)
	}
	if !strings.Contains(ring, "--accent") {
		t.Errorf("the gauges no longer use the CLI accent: %s", ring)
	}

	// Every remaining use of green must belong to an online/health selector.
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, "--green") && !strings.Contains(line, "48,209,88") {
			continue
		}
		switch {
		case strings.Contains(line, ".online"),
			strings.Contains(line, "ping.good"),
			strings.Contains(line, "@keyframes pulse"),
			strings.Contains(line, "--green:"): // the declaration itself
		default:
			t.Errorf("green used outside an online/health indicator:\n  %s", strings.TrimSpace(line))
		}
	}
}

// Every element the script reaches for has to exist in the markup.
//
// $('id') returns null for an element that is not there, and the next property
// access throws — which does not fail loudly, it stops the rest of that
// function running. That is how the theme-picker leftover above broke
// openSettings(): one dead reference, and the dialog silently stopped being
// set up. Adding a field to the panel means adding markup and script in two
// places, so this checks they agree.
func TestEveryScriptedElementExists(t *testing.T) {
	pages := map[string][]byte{
		"dashboard.html": dashboardHTML,
		"login.html":     loginHTML,
	}
	idRe := regexp.MustCompile(`\bid=["']([^"']+)["']`)
	refRe := regexp.MustCompile(`\$\(["']([^"']+)["']\)`)

	for name, page := range pages {
		body := string(page)

		present := map[string]bool{}
		for _, m := range idRe.FindAllStringSubmatch(body, -1) {
			present[m[1]] = true
		}
		for _, m := range refRe.FindAllStringSubmatch(body, -1) {
			if !present[m[1]] {
				t.Errorf("%s: the script reads $(%q) but no element has that id — "+
					"the call returns null and stops the function it is in", name, m[1])
			}
		}
	}
}

// The two notices added for the built-in proxy and for a new release both have
// a quiet state and a loud one, and getting that backwards is the whole failure
// mode: a panel that nags when nothing is wrong gets ignored, and one that
// stays silent when something is wrong is worse than not having the field.
func TestNoticesStayQuietUntilThereIsSomethingToSay(t *testing.T) {
	body := string(dashboardHTML)

	for _, tc := range []struct{ id, gate, why string }{
		{"updbar", "s.updateTag", "the update banner must appear only when a newer release exists"},
		{"pill-proxy", "s.proxyEnabled", "the proxy pill must appear only when the proxy is turned on"},
		{"pill-cong", "s.congestion", "the congestion pill must appear only where the answer is knowable"},
	} {
		// Hidden in the markup, so nothing flashes before the first poll.
		if !regexp.MustCompile(`id="` + tc.id + `"[^>]*style="display:none"`).MatchString(body) {
			t.Errorf("%s is not hidden in the markup — it would show before any data arrives", tc.id)
		}
		if !strings.Contains(body, tc.gate) {
			t.Errorf("%s: %s (no reference to %s)", tc.id, tc.why, tc.gate)
		}
	}
}

// The release tag is a name chosen on GitHub, so it reaches the page as text
// and never as markup. innerHTML here would make whoever can publish a release
// able to run script in the panel.
func TestReleaseTagIsNeverTreatedAsMarkup(t *testing.T) {
	body := string(dashboardHTML)
	for _, bad := range []string{"innerHTML=s.updateTag", "innerHTML = s.updateTag", "innerHTML+=s.updateTag"} {
		if strings.Contains(body, bad) {
			t.Errorf("the release tag is written with %q; it must be set as text", bad)
		}
	}
}

// The header carries only the facts that get looked at repeatedly. OS, location
// and ISP are read when a server is first set up and essentially never again,
// and they were taking half the header for good — on a phone that meant several
// rows of chrome before the first tunnel.
func TestHeaderCarriesOnlyTheFactsWorthPermanentSpace(t *testing.T) {
	body := string(dashboardHTML)

	block := between(body, `class="hostmeta"`, `</div>`+"\n    <div class=\"spacer\"")
	if block == "" {
		t.Fatal("the header host block could not be located")
	}
	for _, id := range []string{"uptime", "ipv4", "ipv6"} {
		if !strings.Contains(block, `id="`+id+`"`) {
			t.Errorf("the header no longer shows %s", id)
		}
	}
	for _, id := range []string{"os", "loc", "isp"} {
		if strings.Contains(block, `id="`+id+`"`) {
			t.Errorf("%s is still in the header; it belongs in the menu", id)
		}
	}
}

// Every menu row goes through menuGo, which closes the menu before opening
// whatever was chosen. A row that calls its panel directly leaves the menu
// sitting open behind it, so the panel has to be dismissed twice.
func TestEveryMenuDestinationClosesTheMenu(t *testing.T) {
	body := string(dashboardHTML)

	nav := between(body, `<nav class="menu"`, `</nav>`)
	if nav == "" {
		t.Fatal("the menu could not be located")
	}
	for _, m := range regexp.MustCompile(`onclick="([^"]+)"`).FindAllStringSubmatch(nav, -1) {
		call := m[1]
		// Logging out navigates away, so nothing is left behind to close.
		if strings.Contains(call, "location.href") || strings.HasPrefix(call, "menuGo(") {
			continue
		}
		t.Errorf("menu row runs %q directly; it should go through menuGo so the menu closes", call)
	}
}

// The panel is used from phones, and until this it had no breakpoints at all —
// the layout survived only because the header could wrap.
func TestLayoutAdaptsToNarrowScreens(t *testing.T) {
	body := string(dashboardHTML)

	if !strings.Contains(body, "@media (max-width:720px)") {
		t.Error("no narrow-screen breakpoint; the layout is desktop-only")
	}
	// On a phone the menu is a sheet across the top rather than a dropdown
	// pinned to a button, which on a short screen opens below the fold.
	mobile := between(body, "@media (max-width:720px)", "@media (max-width:420px)")
	if !strings.Contains(mobile, ".menu{position:fixed") {
		t.Error("the menu is not a full-width sheet on narrow screens")
	}
}

// between returns the text between the first occurrence of start and the next
// occurrence of end after it, or "" when either is missing.
func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	rest := s[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// Settings was eight flat sections: to reach the eighth you scrolled past
// seven, and to find which one held a setting you read all eight. It is five
// collapsed groups now.

// Every group starts closed, and each one has a header wired to the toggle.
// A group whose body is not hidden in the markup is open on first paint, which
// is the flat list again for that section.
func TestSettingsGroupsStartClosed(t *testing.T) {
	body := string(dashboardHTML)

	sections := regexp.MustCompile(`ACC_SECTIONS=\[([^\]]+)\]`).FindStringSubmatch(body)
	if sections == nil {
		t.Fatal("the settings sections list could not be found")
	}
	names := regexp.MustCompile(`'([a-z]+)'`).FindAllStringSubmatch(sections[1], -1)
	if len(names) < 2 {
		t.Fatalf("found %d settings groups, expected several", len(names))
	}

	for _, m := range names {
		id := m[1]
		if !regexp.MustCompile(`id="accb-` + id + `" hidden`).MatchString(body) {
			t.Errorf("the %q group is not hidden in the markup, so it is open on first paint", id)
		}
		if !regexp.MustCompile(`id="acch-` + id + `"[^>]*aria-expanded="false"`).MatchString(body) {
			t.Errorf("the %q header does not report itself collapsed to assistive tech", id)
		}
		if !strings.Contains(body, `toggleAcc('`+id+`')`) {
			t.Errorf("the %q group has no header wired to open it", id)
		}
	}
}

// Opening one group closes the rest. Each is tall enough that two at once
// brings back the scrolling the accordion replaced.
func TestOpeningOneSettingsGroupClosesTheOthers(t *testing.T) {
	toggle := between(string(dashboardHTML), "function toggleAcc(", "\n}")
	if toggle == "" {
		t.Fatal("toggleAcc could not be found")
	}
	if !strings.Contains(toggle, "ACC_SECTIONS.forEach(closeAcc)") {
		t.Error("opening a group does not close the others")
	}
}

// The port and the password are both "how do I get into this panel", and used
// to sit at opposite ends of the list. They are one group now — the merge is
// the point, so it is held down.
func TestPortAndPasswordShareOneGroup(t *testing.T) {
	access := between(string(dashboardHTML), `id="accb-access"`, `</div>`+"\n    </div>")
	if access == "" {
		t.Fatal("the panel-access group could not be found")
	}
	for _, id := range []string{"pport", "np", "np2"} {
		if !strings.Contains(access, `id="`+id+`"`) {
			t.Errorf("%s is not in the panel-access group", id)
		}
	}
}

// Persian support. The panel is used almost entirely by Persian speakers, and
// right-to-left is a property of the page rather than of the words: setting dir
// on the document mirrors a flex and grid layout on its own, so what has to be
// checked is that it is set at all, and that the few rules written with a
// physical side were corrected.
func TestPersianFlipsThePageAndKeepsLatinReadable(t *testing.T) {
	body := string(dashboardHTML)

	if !strings.Contains(body, `html.dir=LANG==='fa'?'rtl':'ltr'`) {
		t.Error("choosing Persian does not set the document direction")
	}
	if !strings.Contains(body, `[dir="rtl"]`) {
		t.Error("no right-to-left rules; the layout will mirror but the hand-placed pieces will not")
	}
	// An address or a port reordered on screen is worse than untranslated text:
	// "127.0.0.1:8080" must not come out backwards inside a Persian page.
	if !strings.Contains(body, "unicode-bidi:plaintext") {
		t.Error("Latin runs inside Persian text are not isolated; addresses and ports will be reordered")
	}
}

// An untranslated string falls back to English rather than to a key or to
// nothing, so a half-finished dictionary degrades to the old wording.
func TestUntranslatedStringsFallBackToEnglish(t *testing.T) {
	fn := between(string(dashboardHTML), "function T(s){", "}")
	if fn == "" {
		t.Fatal("the translation helper could not be found")
	}
	if !strings.Contains(fn, "||s") {
		t.Error("T() does not fall back to the original string")
	}
}

// The language is chosen in the panel and remembered, and the choice is applied
// before first paint — a Persian reader should not see a frame of English and a
// left-to-right layout snap round.
func TestLanguageIsRememberedAndAppliedImmediately(t *testing.T) {
	body := string(dashboardHTML)

	if !strings.Contains(body, "localStorage.setItem('bp_lang'") {
		t.Error("the language choice is not remembered")
	}
	i, j := strings.Index(body, "applyLang(LANG);"), strings.Index(body, "async function loadStats(")
	if i < 0 {
		t.Fatal("the stored language is never applied")
	}
	if j >= 0 && i > j {
		t.Error("the language is applied after the first data load; the page will flash in English")
	}
}

// The bot writes to a person who may not be the one reading the panel, so its
// language is a separate choice and has to reach the API.
func TestBotLanguageIsItsOwnSetting(t *testing.T) {
	body := string(dashboardHTML)

	if !strings.Contains(body, `id="tglang"`) {
		t.Error("there is no bot language control")
	}
	if !strings.Contains(body, "lang:$('tglang').value") {
		t.Error("the bot language is never sent when saving")
	}
	if !strings.Contains(body, "$('tglang').value=t.lang") {
		t.Error("the saved bot language is never read back, so the control shows the wrong value")
	}
}
