package webui

import (
	"os"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/manage"
)

// The stored configurations must never leave the machine.
//
// The history keeps each superseded config verbatim, which is the only way a
// restore can put back a key the current spec cannot hold. Those copies carry
// the tunnel's token. What the panel needs is the moments, and the moments carry
// nothing secret — so the handler builds its own type rather than marshalling
// manage.ConfigChange, which would send Prev with everything in it.
func TestTheConfigHistoryEndpointSendsNoConfigs(t *testing.T) {
	var c confChange
	_ = c

	// The panel's type has no field for the configuration itself. If one is
	// ever added, this is where it gets noticed.
	src := readSourceFile(t, "handlers_confhist.go")
	block := src[strings.Index(src, "type confChange struct"):]
	if end := strings.Index(block, "\n}"); end > 0 {
		block = block[:end]
	}
	for _, leak := range []string{"Prev", "Config", "Toml", "TOML"} {
		if strings.Contains(block, leak) {
			t.Errorf("confChange carries a %q field — the stored configurations hold "+
				"the tunnel's token and must not be sent to a browser", leak)
		}
	}
	if strings.Contains(src, "writeJSON(w, hist)") || strings.Contains(src, "manage.ConfigHistory(name)}") {
		t.Error("the handler marshals manage.ConfigChange directly, which sends the " +
			"stored configuration with it")
	}
}

// Both endpoints change or reveal something about a tunnel, so neither belongs
// to the read-only remote token. Restoring restarts the tunnel.
func TestTheConfigHistoryEndpointsNeedWriteAuth(t *testing.T) {
	src := readSourceFile(t, "server.go")

	for _, route := range []string{"/api/confhist", "/api/confhist/restore"} {
		i := strings.Index(src, `"`+route+`"`)
		if i < 0 {
			t.Fatalf("%s is not registered", route)
		}
		line := src[i:]
		if end := strings.Index(line, "\n"); end > 0 {
			line = line[:end]
		}
		if !strings.Contains(line, "requireAuth") || strings.Contains(line, "requireReadAuth") {
			t.Errorf("%s is not behind requireAuth: %s", route, strings.TrimSpace(line))
		}
	}
}

// A moment identifies an entry, and two edits a second apart are two entries.
// A second-resolution timestamp would restore whichever the list reached first.
func TestAMomentIsPreciseEnoughToIdentifyAnEntry(t *testing.T) {
	src := readSourceFile(t, "handlers_confhist.go")

	if !strings.Contains(src, "UnixNano()") {
		t.Error("the moments are sent at second resolution, so two edits made in the " +
			"same second cannot be told apart")
	}
	if !strings.Contains(src, "time.Unix(0, req.At)") {
		t.Error("the restore does not read the moment back at the resolution it was sent")
	}
}

// The panel must not offer an undo it has nothing for, and must say what the
// undo costs before doing it.
func TestTheUndoListSaysWhatItCosts(t *testing.T) {
	page := string(dashboardHTML)

	body := page[strings.Index(page, "async function loadConfHistory("):]
	if end := strings.Index(body, "\nasync function "); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "if(!list.length) return;") {
		t.Error("an empty history still renders a section, which offers an undo that " +
			"does not exist")
	}

	restore := page[strings.Index(page, "async function restoreConf("):]
	if end := strings.Index(restore, "\n// "); end > 0 {
		restore = restore[:end]
	}
	if !strings.Contains(restore, "confirm(") {
		t.Error("restoring does not confirm, and it restarts the tunnel")
	}
	if !strings.Contains(restore, "drop for a moment") {
		t.Error("the confirmation does not say that connections drop")
	}
}

func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}

var _ = manage.ConfigChange{}
