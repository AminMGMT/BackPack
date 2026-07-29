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

// The accent is chosen by the operator again.
//
// It was fixed for a while, and the tests here used to forbid a picker outright
// — the reasoning being that one colour, matching the CLI, was the decision.
// That has been reversed deliberately. What remains worth guarding is the
// property that made the single accent workable in the first place: every
// coloured thing derives from one variable, so a theme is that variable and
// nothing else. A picker that started overriding individual rules would drift
// out of step one rule at a time.
func TestThemeIsOneVariableAndNothingElse(t *testing.T) {
	body := string(dashboardHTML)

	fn := between(body, "function applyAccent(id){", "\n}")
	if fn == "" {
		t.Fatal("applyAccent could not be found")
	}
	if !strings.Contains(fn, "setProperty('--accent-rgb'") {
		t.Error("applying a theme does not set --accent-rgb")
	}
	// One property, not a list of them.
	if n := strings.Count(fn, "setProperty("); n != 1 {
		t.Errorf("applying a theme writes %d properties; a theme is one variable", n)
	}

	// The default has to be the CLI's colour, so an install nobody has touched
	// looks like the tool it belongs to.
	if !regexp.MustCompile(`\['ember','255,69,58'`).MatchString(body) {
		t.Error("the default accent is no longer the CLI's own colour")
	}
	if !strings.Contains(body, "applyAccent(storedAccent())") {
		t.Error("the stored accent is never applied on load")
	}
}

// The login page must follow the same choice: it is the first thing anyone
// sees, and a sign-in screen in a colour the panel does not use reads as a
// different product.
func TestLoginFollowsTheChosenAccent(t *testing.T) {
	body := string(loginHTML)
	if !strings.Contains(body, "bp_accent") {
		t.Error("the login page ignores the chosen accent")
	}
	if !strings.Contains(body, "setProperty('--accent-rgb'") {
		t.Error("the login page reads the accent but never applies it")
	}
}

func TestNoStaleThemeHandlers(t *testing.T) {
	pages := map[string][]byte{
		"dashboard.html": dashboardHTML,
		"login.html":     loginHTML,
	}
	// The old picker's names, kept banned: a leftover handler is worse than
	// useless, because openSettings() once called one that no longer existed,
	// which threw and stopped the rest of the dialog being set up.
	banned := []string{"applyTheme", "renderSwatches", "THEMES"}

	for name, page := range pages {
		body := string(page)
		for _, token := range banned {
			if strings.Contains(body, token) {
				t.Errorf("%s still references the removed theme picker (%q)", name, token)
			}
		}
	}

	// An accent saved by an older build must still resolve to something. The
	// names changed when the picker came back, so an unrecognised one has to
	// fall through to the default rather than leaving the page uncoloured.
	if !strings.Contains(string(dashboardHTML), "||ACCENTS[0]") {
		t.Error("an unknown saved accent does not fall back to the default")
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

	// Each page declares exactly one accent in CSS — the default. Alternatives
	// live in a script table and are applied at runtime, never as a second
	// declaration that could drift from the first.
	if len(dash) != 1 {
		t.Errorf("dashboard.html declares %d accents in CSS, want exactly 1: %v", len(dash), dash)
	}
	if len(login) != 1 {
		t.Errorf("login.html declares %d accents in CSS, want exactly 1: %v", len(login), login)
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
			// The online pulse, on the status dot and on the ring around the
			// card that breathes with it.
			strings.Contains(line, "@keyframes pulse"),
			strings.Contains(line, "@keyframes ring-on"),
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
		{"hm-proxy", "s.proxyEnabled", "the proxy row must appear only when the proxy is turned on"},
		{"hm-cong", "s.congestion", "the congestion row must appear only where the answer is knowable"},
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

// The header carries the brand and the way in, and nothing else.
//
// Every fact about the machine — uptime, addresses, OS, location, ISP — is read
// when a server is set up and then almost never again, so none of it earns a
// permanent strip across the top of every screen. It all moved into the menu.
func TestHeaderCarriesNothingButTheBrand(t *testing.T) {
	body := string(dashboardHTML)

	header := between(body, "<header>", "</header>")
	if header == "" {
		t.Fatal("the header could not be located")
	}
	for _, id := range []string{"uptime", "ipv4", "ipv6", "os", "loc", "isp"} {
		if strings.Contains(header, `id="`+id+`"`) {
			t.Errorf("%s is still in the header; it belongs in the menu", id)
		}
	}

	// They have to be somewhere, or the fix is a deletion.
	menu := between(body, `<nav class="menu"`, "</nav>")
	for _, id := range []string{"uptime", "ipv4", "ipv6", "os", "loc", "isp"} {
		if !strings.Contains(menu, `id="`+id+`"`) {
			t.Errorf("%s was removed from the header but never added to the menu", id)
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

// The menu and its backdrop must not live inside <header>.
//
// The header carries a backdrop-filter, and a filtered element becomes the
// containing block for every position:fixed descendant. A backdrop written to
// cover the viewport therefore covered only the header, so clicking anywhere
// below it did nothing and the menu would not close; the same trap
// mispositioned the full-width sheet on phones. Both are laid out against the
// viewport now, which is what they were written to assume.
//
// Nothing about the CSS looks wrong in either arrangement, which is exactly why
// this is pinned.
func TestMenuIsNotTrappedInsideTheFilteredHeader(t *testing.T) {
	body := string(dashboardHTML)

	header := between(body, "<header>", "</header>")
	if header == "" {
		t.Fatal("the header could not be located")
	}
	if !strings.Contains(header, "backdrop-filter") && !strings.Contains(body, "header{") {
		t.Skip("the header no longer appears to be filtered")
	}
	for _, id := range []string{"menuscrim", "mainmenu"} {
		if strings.Contains(header, `id="`+id+`"`) {
			t.Errorf("%s is inside the filtered header; position:fixed will be measured "+
				"against the header instead of the viewport", id)
		}
	}
	// And it has to be positioned, or it lands wherever the last layout left it.
	if !strings.Contains(body, "function placeMenu(") {
		t.Error("nothing positions the menu once it is fixed to the viewport")
	}
}

// Persian is the language nearly every operator of this panel reads, so a
// string that was never added to the dictionary is a real gap rather than a
// cosmetic one. This walks the markup and reports anything marked translatable
// that has no Persian, which is the check that stops the dictionary quietly
// falling behind the page.
func TestEveryMarkedStringHasPersian(t *testing.T) {
	body := string(dashboardHTML)

	// Look at the markup only: the script contains the dictionary itself.
	_, rest, ok := strings.Cut(body, "</style>")
	if !ok {
		t.Fatal("could not separate the stylesheet")
	}
	markup, _, ok := strings.Cut(rest, "<script>")
	if !ok {
		t.Fatal("could not separate the script")
	}

	dict := between(body, "const FA={", "\n};")
	if dict == "" {
		t.Fatal("the Persian dictionary could not be found")
	}

	// Proper nouns that must not be translated: ticker symbols and chain names.
	skip := regexp.MustCompile(`TRX|BEP20|TON|IPv4|IPv6`)

	space := regexp.MustCompile(`\s+`)
	for _, m := range regexp.MustCompile(`data-i18n[^>]*>([^<]+)<`).FindAllStringSubmatch(markup, -1) {
		text := space.ReplaceAllString(strings.TrimSpace(m[1]), " ")
		if text == "" || skip.MatchString(text) {
			continue
		}
		if !strings.Contains(dict, `"`+text+`"`) {
			t.Errorf("no Persian for %q — it will stay English when the panel is switched", text)
		}
	}
}

// Markup injected after the first pass — tunnel rows, details, check results —
// has to be swept again, or half the page reverts to English the moment it
// updates.
func TestDynamicMarkupIsTranslatedToo(t *testing.T) {
	body := string(dashboardHTML)

	if !strings.Contains(body, "function relang()") {
		t.Fatal("nothing re-applies the language after a render")
	}
	for _, site := range []string{
		"renderDetails(t); relang();",
		"if(built) relang();",
	} {
		if !strings.Contains(body, site) {
			t.Errorf("markup rendered at %q is never re-translated", site)
		}
	}
}

// The login page has no settings of its own, so it follows the choice already
// stored. An English door on a Persian panel is the first thing anybody sees.
func TestLoginPageFollowsTheStoredLanguage(t *testing.T) {
	body := string(loginHTML)

	if !strings.Contains(body, "localStorage.getItem('bp_lang')") {
		t.Error("the login page ignores the language the panel was left in")
	}
	if !strings.Contains(body, "dir='rtl'") {
		t.Error("the login page never flips to right-to-left")
	}
	// The password itself is Latin and must not be reordered by the flip.
	if !strings.Contains(body, "pw.style.direction='ltr'") {
		t.Error("the password field is not kept left-to-right")
	}
}

// The menu floats over the tunnel cards, so it needs a surface of its own.
//
// It was originally given --card, which is what every panel in the page uses.
// That is right for something sitting in the page and wrong for something over
// it: at rgba(255,255,255,.045) the cards behind showed straight through the
// labels. A blur does not rescue it either — blurring bright content leaves it
// bright. Reaching for --card here is the natural mistake, so it is pinned.
func TestMenuHasItsOwnOpaqueSurface(t *testing.T) {
	body := string(dashboardHTML)

	menu := between(body, ".menu{position:fixed", "}")
	if menu == "" {
		t.Fatal("the menu rule could not be found")
	}
	if strings.Contains(menu, "background:var(--card)") {
		t.Error("the menu uses the page's translucent panel colour; text behind it shows through")
	}

	// Whatever it does use has to be substantially opaque.
	m := regexp.MustCompile(`background:rgba\(\d+,\s*\d+,\s*\d+,\s*\.(\d+)\)`).FindStringSubmatch(menu)
	if m == nil {
		t.Fatalf("could not read the menu background out of %q", menu)
	}
	// ".97" -> 97, ".9" -> 90
	alpha := m[1]
	if len(alpha) == 1 {
		alpha += "0"
	}
	if alpha < "85" {
		t.Errorf("the menu background is %s%% opaque; the cards behind it will still read through", alpha)
	}

	// The blur is still worth keeping for the edges.
	if !strings.Contains(menu, "backdrop-filter:blur(") {
		t.Error("the menu lost its backdrop blur")
	}
}

// The star prompt has to be easy to get rid of.
//
// This panel also carries the alerts, so anything that trains someone to
// dismiss it without reading costs more than a star is worth. Three properties
// keep it tolerable: it never appears on a first visit, it waits days between
// asks, and both "no" answers are final.
func TestStarPromptCanAlwaysBeSilenced(t *testing.T) {
	body := string(dashboardHTML)

	fn := between(body, "function maybeAskForStar(){", "\n}")
	if fn == "" {
		t.Fatal("the star prompt logic could not be found")
	}
	if !strings.Contains(fn, "if(st.done) return;") {
		t.Error("a dismissed prompt can come back")
	}
	if !strings.Contains(fn, "st.first") {
		t.Error("the prompt does not skip the first visit; it would beg before it has been useful")
	}
	if !strings.Contains(fn, "STAR_EVERY") {
		t.Error("nothing spaces the prompt out — it could appear on every load")
	}

	// Both ways of saying no have to stick, and following the link counts as
	// done: GitHub never tells us whether the star was given, and asking again
	// after someone has starred is worse than not asking.
	for _, fname := range []string{"function starNever(){", "function starOnGitHub(){"} {
		f := between(body, fname, "\n}")
		if f == "" {
			t.Errorf("%s not found", fname)
			continue
		}
		if !strings.Contains(f, "done:true") {
			t.Errorf("%s does not record the answer, so the prompt returns in three days", fname)
		}
	}

	// The interval is stated in the source; a prompt every few hours would pass
	// every check above and still be a nuisance.
	if !regexp.MustCompile(`STAR_EVERY\s*=\s*3\s*\*\s*24\s*\*\s*3600\s*\*\s*1000`).MatchString(body) {
		t.Error("the prompt interval is not three days")
	}
}

// The accent picker belongs in Settings, next to Language.
//
// It was first put in the menu, beside the machine's facts, because that is
// where those had just moved. Nobody found it: how the panel looks is a
// setting, and Settings is where people go to look for one. Both are opened
// from the same button, which made it easy to miss and easy to get wrong.
func TestAccentPickerIsInSettingsBesideLanguage(t *testing.T) {
	body := string(dashboardHTML)

	settings := between(body, `id="settings"`, `id="support"`)
	if settings == "" {
		t.Fatal("the settings dialog could not be located")
	}
	if !strings.Contains(settings, `id="accentrow"`) {
		t.Error("the accent picker is not in Settings — that is where people look for it")
	}

	// Beside Language: the two are the same kind of choice, and separating them
	// means one of them is somewhere unexpected.
	lang := strings.Index(settings, `id="langsel"`)
	accent := strings.Index(settings, `id="accentrow"`)
	if lang < 0 || accent < 0 {
		t.Fatal("Language or the accent picker is missing from Settings")
	}
	if between(settings[lang:accent], `<div class="acc">`, "") != "" {
		t.Error("a settings group sits between Language and Theme; they should be adjacent")
	}

	// Six unlabelled dots are a guessing game, so the chosen one is named.
	if !strings.Contains(settings, `id="accentname"`) {
		t.Error("the chosen accent is never named")
	}
}
