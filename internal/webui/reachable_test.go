package webui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every door the panel exposes has to have something on the other side of it.
//
// This exists because the same bug was found by hand three times in one
// sitting, each time by accident:
//
//   - `nodeUpgradeAll` was exported from api.js and called from nowhere. The
//     fleet upgrade had no button at all.
//   - `nodeRolloutCancel` was added with its endpoint and never wired to
//     anything, so a rollout could be started and not stopped.
//   - the whole share-link codec is reachable from nothing but itself.
//
// None of them failed a test, none of them failed a build, and none of them
// would ever have been noticed by a person using the panel — because the way
// you notice a missing button is by looking for one, and nobody looks for a
// button they do not know should exist.
//
// The rule is narrow on purpose: an api.js export is a call the panel is meant
// to make. If nothing makes it, either the UI is missing or the export is
// dead, and both are worth a failing test.

var (
	jsExport = regexp.MustCompile(`(?m)^export const (\w+)\s*=`)
	// A call, through the namespace import the views use (`api.foo(`) or
	// bare after a named import.
	callOf = func(name string) *regexp.Regexp {
		return regexp.MustCompile(`\b(api\.)?` + regexp.QuoteMeta(name) + `\s*\(`)
	}
)

func TestEveryPanelAPIFunctionIsCalled(t *testing.T) {
	root := filepath.Join("panel", "js")
	apiPath := filepath.Join(root, "api.js")

	src, err := os.ReadFile(apiPath)
	if err != nil {
		t.Fatalf("reading %s: %v", apiPath, err)
	}

	// Everything else under panel/js, which is where a call would be.
	var callers []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".js") {
			return err
		}
		if path == apiPath {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		callers = append(callers, string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(callers) < 5 {
		t.Fatalf("found only %d files under %s — this guard is looking in the wrong place",
			len(callers), root)
	}
	all := strings.Join(callers, "\n")

	exports := jsExport.FindAllStringSubmatch(string(src), -1)
	if len(exports) < 20 {
		t.Fatalf("found only %d exports in api.js — the pattern has stopped matching", len(exports))
	}

	for _, m := range exports {
		name := m[1]
		if callOf(name).MatchString(all) {
			continue
		}
		t.Errorf("api.%s is exported and nothing calls it.\n"+
			"Either the panel is missing the control that should call it — which is how "+
			"the fleet upgrade shipped with no button — or it is dead and should go. "+
			"A door with nothing behind it fails no build and no test, and nobody looks "+
			"for a button they do not know should exist.", name)
	}
}

// The same rule in the other direction: an action the server handles has to be
// one the panel can actually send.
//
// This half is deliberately weaker. The node endpoint multiplexes a dozen
// operations behind one path and some are reached by a script or by curl rather
// than by the panel, so an unmatched action is reported and not failed — it is
// a list to read, not a gate.
func TestEveryNodeActionHasACaller(t *testing.T) {
	src, err := os.ReadFile("handlers_nodes.go")
	if err != nil {
		t.Fatalf("reading handlers_nodes.go: %v", err)
	}
	actions := regexp.MustCompile(`(?m)^\tcase "(\w+)":`).FindAllStringSubmatch(string(src), -1)
	if len(actions) < 5 {
		t.Fatalf("found %d actions — the pattern has stopped matching", len(actions))
	}

	api, err := os.ReadFile(filepath.Join("panel", "js", "api.js"))
	if err != nil {
		t.Fatalf("reading api.js: %v", err)
	}

	for _, m := range actions {
		action := m[1]
		if strings.Contains(string(api), `'`+action+`'`) || strings.Contains(string(api), `"`+action+`"`) {
			continue
		}
		t.Logf("note: the node action %q is handled by the server and nothing in api.js "+
			"sends it — reachable by curl, but not from the panel", action)
	}
}
