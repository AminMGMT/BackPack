package webui

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The panel's screen map has to describe the panel that exists.
//
// docs/web-panel-screens.md is the only complete account of what the panel can
// show — the panel itself is a single-page application with no sitemap, and a
// screen that is never written down is a screen an operator never finds. A
// document like that is worth exactly as much as its accuracy, and nothing
// about adding a route to main.js makes anybody open it.
//
// So the two are compared. Every route registered in the panel has a line in
// the document, and every address the document quotes is a route. The document
// writes a tunnel screen as "#/t/fr-relay/logs" where the code registers
// "/t/:name/logs", so the name is normalised away on both sides before they are
// compared; nothing else is.

var (
	routeRE = regexp.MustCompile(`router\.route\('([^']+)'`)
	// Addresses in the document are in backticks: `#/t/fr-relay/metrics`.
	docAddrRE = regexp.MustCompile("`(#/[^`]*)`")
	// Any :param in a pattern, or the example name in the document.
	paramRE = regexp.MustCompile(`/t/[^/]+/`)
)

func normalise(path string) string {
	path = strings.TrimPrefix(path, "#")
	return paramRE.ReplaceAllString(path, "/t/NAME/")
}

func TestTheScreenMapDescribesEveryPanelRoute(t *testing.T) {
	main, err := os.ReadFile(filepath.Join("panel", "js", "main.js"))
	if err != nil {
		t.Fatalf("reading the panel entry point: %v", err)
	}
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "web-panel-screens.md"))
	if err != nil {
		t.Fatalf("reading the screen map: %v", err)
	}

	inCode := map[string]bool{}
	for _, m := range routeRE.FindAllStringSubmatch(string(main), -1) {
		inCode[normalise(m[1])] = true
	}
	if len(inCode) < 10 {
		t.Fatalf("found %d routes in main.js — this test is reading the wrong file", len(inCode))
	}

	inDoc := map[string]bool{}
	for _, m := range docAddrRE.FindAllStringSubmatch(string(doc), -1) {
		inDoc[normalise(m[1])] = true
	}

	for _, path := range sorted(inCode) {
		if !inDoc[path] {
			t.Errorf("the panel has a screen at %s that docs/web-panel-screens.md does not "+
				"mention. A screen nobody wrote down is a screen nobody finds.", path)
		}
	}
	for _, path := range sorted(inDoc) {
		if !inCode[path] {
			t.Errorf("docs/web-panel-screens.md describes a screen at %s that the panel no "+
				"longer has. Wrong documentation about a user interface is worse than none.", path)
		}
	}
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
