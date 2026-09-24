package webui

import (
	"os"
	"regexp"
	"testing"
)

// Every drop-down on the settings screen has choices to drop down.
//
// A menu is wired only if settings.js lists its choices by the element's
// data-name; one that is not listed draws its placeholder and never opens.
// The API token's scope was one of those, so every token the panel issued was
// read-only whatever the operator meant — and nothing said so.
func TestEverySettingsMenuHasChoices(t *testing.T) {
	html, err := os.ReadFile("panel/views/settings.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := os.ReadFile("panel/js/views/settings.js")
	if err != nil {
		t.Fatal(err)
	}
	menus := regexp.MustCompile(`class="sel" data-name="([A-Za-z]+)"`).FindAllSubmatch(html, -1)
	if len(menus) < 4 {
		t.Fatalf("found %d menus in settings.html — the reader is broken", len(menus))
	}
	for _, m := range menus {
		name := string(m[1])
		if !regexp.MustCompile(`(?m)^\s+` + name + `: \[`).Match(js) {
			t.Errorf("the %q menu has no choices in settings.js, so it never opens", name)
		}
	}
}

// The scope is read from the value the menu set, not guessed from its label.
func TestTheTokenScopeIsTheOnePicked(t *testing.T) {
	js, err := os.ReadFile("panel/js/views/settings.js")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`\[data-name="tokScope"\]'\)\?\.dataset\.value`).Match(js) {
		t.Error("the token form does not read the scope the menu set")
	}
	for _, scope := range []string{"read", "write", "admin"} {
		if !regexp.MustCompile(`value: '` + scope + `'`).Match(js) {
			t.Errorf("the scope menu does not offer %s", scope)
		}
	}
}
