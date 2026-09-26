package webui

import (
	"io/fs"
	"os"
	"strings"
	"testing"
)

// A refusal that has one remedy offers it, rather than describing it.
//
// The link test needs the server holding the tunnel's other end, and refuses
// when the panel does not know which server that is. Saying "link it" in a sentence puts the action
// two screens away and makes knowing it exists the hard part; the operator who
// reported this had been told, twice, and still could not run either.
func TestARefusalWithARemedyCarriesIt(t *testing.T) {
	// The server names the remedy in a field, not in the prose. Keying on the
	// words would be the same mistake as keying on "not installed" was: the
	// sentence is written for a person, and rewording it must not silently take
	// the button away.
	for _, f := range []string{"handlers_monitoring.go"} {
		src, err := readRepoFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if !strings.Contains(string(src), "fixHeader") {
			t.Errorf("%s refuses without naming what would fix it", f)
		}
	}

	loadPanel()

	// The browser carries it onto the error.
	api, err := fs.ReadFile(panelRoot, "js/api.js")
	if err != nil {
		t.Fatalf("api.js: %v", err)
	}
	if !strings.Contains(string(api), "X-Backpack-Fix") {
		t.Error("the panel throws away the remedy the server named")
	}

	// The toast turns it into something to press.
	toast, err := fs.ReadFile(panelRoot, "js/ui/toast.js")
	if err != nil {
		t.Fatalf("toast.js: %v", err)
	}
	for _, want := range []string{"link-tunnel", "onFix", "tact"} {
		if !strings.Contains(string(toast), want) {
			t.Errorf("the error toast cannot offer a remedy (%q missing)", want)
		}
	}

	// And something registers what pressing it does.
	dash, err := fs.ReadFile(panelRoot, "js/views/dashboard.js")
	if err != nil {
		t.Fatalf("dashboard.js: %v", err)
	}
	if !strings.Contains(string(dash), "onFix.link") {
		t.Error("nothing acts on the remedy, so the button would do nothing")
	}

	// The two screens that can be refused pass the tunnel they were refused
	// about — a button that does not know which tunnel cannot link it.
	lt, _ := fs.ReadFile(panelRoot, "js/views/linktest.js")
	if !strings.Contains(string(lt), "oops(e, name)") {
		t.Error("the link test's refusal does not say which tunnel it was about")
	}
}

func readRepoFile(name string) ([]byte, error) { return os.ReadFile("./" + name) }
