package webui

import (
	"io/fs"
	"strings"
	"testing"
)

// The overview's quick actions, and the header button they replaced.
//
// The header carried a menu holding Settings, Health check, Maintenance,
// GitHub and the switch back to the classic panel. Every one of those had, or
// could have, another door: the first two are worth a button on the front page,
// Maintenance is what Settings' own Update pane is about, the classic switch is
// a panel-access choice, and GitHub is in the overview's footer. A button that
// opens a list of doors that all have another one is a button that is only in
// the way — so it is Log out now, which is the one thing with no other route.
//
// The risk in that change is stranding a screen. These check that nothing was
// removed without somewhere to go.
func TestTheOverviewOffersTheQuickActions(t *testing.T) {
	loadExperimentalPanel()

	b, err := fs.ReadFile(panelRoot, "js/views/overview.js")
	if err != nil {
		t.Fatalf("cannot read overview.js: %v", err)
	}
	src := string(b)

	for _, want := range []struct{ label, to string }{
		{"Health check", "/health"},
		{"Settings", "/settings"},
		{"Support", "/support"},
		{"Bug reports", "https://github.com/AminMGMT/BackPack/issues"},
	} {
		if !strings.Contains(src, `'`+want.label+`'`) {
			t.Errorf("the overview has no %q action", want.label)
		}
		if !strings.Contains(src, want.to) {
			t.Errorf("the %q action points nowhere: %q is not in overview.js", want.label, want.to)
		}
	}

	// Each one carries its own icon, and an icon that is not in the set draws
	// nothing at all — svg() returns an empty string for a name it does not
	// know, so a typo here is four buttons with a hole in them.
	icons, err := fs.ReadFile(panelRoot, "js/lib/icons.js")
	if err != nil {
		t.Fatalf("cannot read icons.js: %v", err)
	}
	for _, name := range []string{"pulse", "gear", "life", "bug"} {
		if !strings.Contains(string(icons), name+":") {
			t.Errorf("the icon set has no %q, so that action draws no icon", name)
		}
	}
}

// Nothing the menu held may be left without a route.
func TestNothingTheHeaderMenuHeldWasStranded(t *testing.T) {
	loadExperimentalPanel()

	if _, err := fs.ReadFile(panelRoot, "js/ui/menu.js"); err == nil {
		// The menu is allowed to exist; what is not allowed is losing the
		// screens it held. Nothing to check here beyond the rest of this test.
		t.Log("the header menu is still present")
	}

	index, err := fs.ReadFile(panelRoot, "index.html")
	if err != nil {
		t.Fatalf("cannot read index.html: %v", err)
	}
	head := string(index)
	if strings.Contains(head, `id="menu-btn"`) && !strings.Contains(head, `id="logout-btn"`) {
		t.Error("the header still opens a menu and offers no way to log out")
	}
	if !strings.Contains(head, "/logout") {
		t.Error("there is no way to log out of the panel")
	}

	// Maintenance and the classic-panel switch lost their only entrance when
	// the menu went, so both have to be somewhere the panel serves.
	for _, want := range []struct{ what, needle string }{
		{"the Maintenance screen", "/maintenance"},
		{"the switch back to the classic panel", "/?panel=classic"},
	} {
		found := false
		err := fs.WalkDir(panelRoot, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if !strings.HasSuffix(p, ".js") && !strings.HasSuffix(p, ".html") {
				return nil
			}
			f, err := fs.ReadFile(panelRoot, p)
			if err != nil {
				return err
			}
			if strings.Contains(string(f), want.needle) {
				found = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking the panel: %v", err)
		}
		if !found {
			t.Errorf("%s can no longer be reached from anywhere in the panel", want.what)
		}
	}

	// A row that says it opens a screen needs something that opens it. The
	// settings screen builds none of its markup, so the handler is delegated;
	// without it the row is a button that does nothing.
	set, err := fs.ReadFile(panelRoot, "js/views/settings.js")
	if err != nil {
		t.Fatalf("cannot read settings.js: %v", err)
	}
	if !strings.Contains(string(set), "[data-to]") {
		t.Error("settings has rows marked data-to and nothing that acts on them")
	}
}
