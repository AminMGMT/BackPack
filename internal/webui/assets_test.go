package webui

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The login page must follow the panel's appearance: it is the first thing
// anyone sees, and a sign-in screen in a colour or ground the panel does not
// use reads as a different product. It reads the same keys the panel stores
// (js/main.js) and knows every accent the panel offers.
func TestLoginFollowsThePanelAppearance(t *testing.T) {
	accents, err := os.ReadFile("panel/css/accent.css")
	if err != nil {
		t.Fatal(err)
	}
	for name, page := range map[string][]byte{"login": loginHTML, "two-factor": twoFactorHTML} {
		body := string(page)
		for _, want := range []string{"bp_theme", "bp_accent", "dataset.t=", "dataset.accent="} {
			if !strings.Contains(body, want) {
				t.Errorf("the %s page does not follow the panel's appearance: %q is missing", name, want)
			}
		}
		for _, m := range regexp.MustCompile(`data-accent="([a-z]+)"`).FindAllStringSubmatch(string(accents), -1) {
			if m[1] != "none" && !strings.Contains(body, `data-accent="`+m[1]+`"`) {
				t.Errorf("the %s page has no colour for the panel's %q accent", name, m[1])
			}
		}
	}
}

// A refused attempt says so. The page used to reappear unchanged, which reads
// as the click not having worked.
func TestARefusedSignInSaysSo(t *testing.T) {
	for name, page := range map[string][]byte{"login": loginHTML, "two-factor": twoFactorHTML} {
		if !strings.Contains(string(page), loginStatePlaceholder) {
			t.Errorf("the %s page has no place for the refused state", name)
		}
		if got := string(withLoginState(page, true)); !strings.Contains(got, `<html lang="en" data-state="wrong">`) {
			t.Errorf("the %s page is not marked after a refusal", name)
		}
		if got := string(withLoginState(page, false)); strings.Contains(got, loginStatePlaceholder) ||
			!strings.Contains(got, `<html lang="en" data-state="">`) {
			t.Errorf("the %s page shows a refusal nobody made", name)
		}
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
