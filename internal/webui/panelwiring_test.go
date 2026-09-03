package webui

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Controls that look like controls.
//
// This panel was built from a preview, and a preview draws things rather than
// making them work: a switch is a <div class="sw3">, a dropdown is a
// <div class="sel4"> with a chevron in it, and a button carries onclick="fn()"
// where fn was a function in the preview's own script and is not in this one.
// None of that fails a build or a request. It renders perfectly and does
// nothing, and the only way it has ever been found is somebody pressing it.
//
// Forty-odd controls were in that state when this was written — the settings
// search, the whole speed test, two of the three buttons on the log toolbar,
// every switch and menu in the advanced drawers. So the rule is checked here
// instead: a control either names a handler this panel has, or is wired by one
// of the conventions the views use, or it is not shipped.

// genericVerbs are the behaviours ui/screen.js restores for every screen, so a
// view does not have to name them.
var genericVerbs = map[string]bool{
	"tab": true, "setPane": true, "go": true, "toast": true,
	"dr": true, "dr2": true, "dr3": true,
}

func TestEveryControlInThePanelIsWiredToSomething(t *testing.T) {
	loadExperimentalPanel()

	// Every line of the panel's own script, which is where a handler has to be.
	var js strings.Builder
	err := fs.WalkDir(panelRoot, "js", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") {
			return err
		}
		b, err := fs.ReadFile(panelRoot, p)
		if err != nil {
			return err
		}
		js.Write(b)
		js.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatalf("reading the panel's scripts: %v", err)
	}
	script := js.String()

	views, err := fs.ReadDir(panelRoot, "views")
	if err != nil {
		t.Fatalf("reading views: %v", err)
	}

	onclickEl := regexp.MustCompile(`<[^>]*onclick="([A-Za-z_$][\w$]*)\([^>]*>`)
	// Every class the previews used for a switch or a menu. They differ per
	// screen because each preview was drawn on its own — sw3/sel4 on the setup
	// form, sww/sel on the edit dialog — and a class this list does not know is
	// a screenful of controls nobody checks.
	control := regexp.MustCompile(`<div class="(sw3|sel4|sww2|sww|sel)[^"]*"([^>]*)>`)
	idAttr := regexp.MustCompile(`id="([^"]+)"`)
	// The drawers name their field with a prefix; see wireDrawerControls.
	drawerID := regexp.MustCompile(`^(ft|sp|pk|cn|dr)-\w`)

	var dead, unnamed []string
	for _, v := range views {
		name := v.Name()
		if !strings.HasSuffix(name, ".html") {
			continue
		}
		// login.html in views/ is preview markup for a page the server does not
		// serve — the panel is reached through the existing /login. It has no
		// script of its own and is not expected to.
		if name == "login.html" {
			continue
		}
		raw, err := fs.ReadFile(panelRoot, "views/"+name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		html := string(raw)

		// The whole element, so the id beside the onclick can be seen.
		for _, m := range onclickEl.FindAllStringSubmatch(html, -1) {
			el, verb := m[0], m[1]
			if genericVerbs[verb] || strings.HasPrefix(strings.ToLower(verb), "close") {
				continue
			}
			// Wired by the verb — a view names it, or handles it in its pick
			// listener — or by the id, which is how the views that were written
			// after the preview bind things.
			if strings.Contains(script, verb) {
				continue
			}
			if id := idAttr.FindStringSubmatch(el); id != nil && strings.Contains(script, id[1]) {
				continue
			}
			dead = append(dead, name+": onclick=\""+verb+"()\"")
		}

		for _, m := range control.FindAllStringSubmatch(html, -1) {
			attrs := m[2]
			if strings.Contains(attrs, "data-name=") {
				continue
			}
			id := idAttr.FindStringSubmatch(attrs)
			if id != nil && (drawerID.MatchString(id[1]) || strings.Contains(script, id[1])) {
				continue
			}
			what := m[1]
			if id != nil {
				what += " #" + id[1]
			}
			unnamed = append(unnamed, name+": "+what)
		}
	}
	sort.Strings(dead)
	sort.Strings(unnamed)

	for _, d := range dead {
		t.Errorf("%s — nothing in the panel's script defines that handler, so the "+
			"control renders and does nothing", d)
	}
	for _, u := range unnamed {
		t.Errorf("%s has no name, no data-name and no id the wiring recognises — it "+
			"is drawn as a control and posts nothing", u)
	}
}

// The panel and the server have to agree on what a tunnel's state is called.
//
// They did not: manage.Health reports "online", "offline" and "stopped", and
// six screens compared against "running", which nothing produces. A tunnel that
// was up read as Stopped on its card, counted as down on the overview, and was
// offered by neither the speed test nor the link test. One module now holds the
// vocabulary, and this keeps the screens from going around it again.
func TestTheScreensReadTunnelStateThroughOnePlace(t *testing.T) {
	const lib = "panel/js/lib/tstate.js"
	src, err := os.ReadFile(lib)
	if err != nil {
		t.Fatalf("read %s: %v", lib, err)
	}
	for _, want := range []string{"online", "offline", "stopped"} {
		if !bytes.Contains(src, []byte(`'`+want+`'`)) && !bytes.Contains(src, []byte(want+":")) {
			t.Errorf("%s does not account for the %q the server sends", lib, want)
		}
	}

	// Nowhere else may compare .state to a literal: that is how the panel drifted.
	cmp := regexp.MustCompile(`\.state\s*[!=]==\s*'([a-z]+)'`)
	err = fs.WalkDir(os.DirFS("panel/js"), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") || p == "lib/tstate.js" {
			return err
		}
		b, err := os.ReadFile(filepath.Join("panel/js", p))
		if err != nil {
			return err
		}
		for _, m := range cmp.FindAllSubmatch(b, -1) {
			t.Errorf("panel/js/%s compares a tunnel's state to %q by hand — use lib/tstate.js", p, m[1])
		}
		// A module is one file: using the helper without importing it is a
		// ReferenceError at render time, and the screen simply does not appear.
		if bytes.Contains(b, []byte("isUp(")) && !bytes.Contains(b, []byte("lib/tstate.js")) {
			t.Errorf("panel/js/%s calls isUp() without importing lib/tstate.js", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
