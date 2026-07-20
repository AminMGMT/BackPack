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
