package webui

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// A navigation is a request too.
//
// TestThePanelAsksUnderThePathItIsServedFrom covers every address the panel
// fetches, and that is where the checking stopped — so the two addresses it
// reaches by navigating to them instead went unchecked, and both were wrong.
// Downloading a backup and signing out after a password change are navigations:
// the browser is sent to an address rather than asked for one. Both were sent to
// the root of the origin, which is the one place the panel does not answer.
//
// What that cost is not cosmetic. The backup is what an operator is told to take
// before an update, and both of its Download buttons 404'd. The sign-out is the
// second half of changing the panel password — the page says every device is
// signed out, including this one — and it left the browser holding a live
// session for a password that no longer existed.
func TestEveryNavigationThePanelMakesCarriesTheBasePath(t *testing.T) {
	loadPanel()

	// location.href = '/...', location.assign('/...'), window.open('/...').
	// An address that starts with a slash and is not built from the base is an
	// address at the root of the origin.
	absolute := regexp.MustCompile(`(?:location\.href\s*=|location\.assign\(|window\.open\()\s*['"` + "`" + `]/`)

	for _, f := range panelScripts(t) {
		b, err := fs.ReadFile(panelRoot, f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if absolute.MatchString(line) {
				t.Errorf("%s navigates to an absolute path, which leaves the panel:\n  %s",
					f, strings.TrimSpace(line))
			}
		}
	}
}

// api.js is the only file that knows where the panel is served from, so a
// helper there that hands back a URL has to apply it — the helpers that hand
// back a promise already do, through get and post.
func TestEveryURLApiJsHandsOutCarriesTheBasePath(t *testing.T) {
	loadPanel()

	b, err := fs.ReadFile(panelRoot, "js/api.js")
	if err != nil {
		t.Fatalf("api.js: %v", err)
	}
	// export const somethingURL = () => ...
	urlHelper := regexp.MustCompile(`export const (\w*URL)\s*=[^;]*`)
	found := 0
	for _, m := range urlHelper.FindAllStringSubmatch(string(b), -1) {
		found++
		if !strings.Contains(m[0], "at(") && !strings.Contains(m[0], "BASE") {
			t.Errorf("api.js: %s builds an address without the base path — a link or a "+
				"download pointed at it lands where the panel does not answer", m[1])
		}
	}
	if found == 0 {
		t.Error("no URL helper found in api.js — this test has stopped checking anything")
	}
}

// panelScripts lists every script the panel ships, so a view added later is
// covered without anybody remembering to add it here. Naming the files one by
// one is what let api.logs build its own URL and 404 every Logs button while
// the checked calls all still passed.
func panelScripts(t *testing.T) []string {
	t.Helper()
	var out []string
	err := fs.WalkDir(panelRoot, "js", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && path.Ext(p) == ".js" {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the panel scripts: %v", err)
	}
	if len(out) < 10 {
		t.Fatalf("found only %d panel scripts — the walk is not seeing them", len(out))
	}
	return out
}
