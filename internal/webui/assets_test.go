package webui

import (
	"strings"
	"testing"
)

// The panel is deliberately single-themed: one accent, matching the CLI menu.
// These tests guard that decision, because it is the kind of thing a later edit
// re-adds without meaning to — a colour picker looks like a feature, and the
// reason it is absent lives in the changelog rather than the code.

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
