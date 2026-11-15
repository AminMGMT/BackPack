package webui

import (
	"os"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/manage"
)

// The health API names a failed check "fail"; the panel used to look for
// "error", which never arrives, so a failed check — the engine binary missing —
// was drawn with the green dot and the tally read "0 broken". Both ends of
// that contract are pinned here.
func TestAFailedHealthCheckReachesThePanelAsBroken(t *testing.T) {
	if got := healthLevel(manage.CheckFail); got != "fail" {
		t.Fatalf("the API calls a failure %q", got)
	}
	src, err := os.ReadFile("panel/js/views/monitor.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "l === 'fail' ? 'error'") {
		t.Error("monitor.js no longer maps the API's \"fail\" onto its broken row and tally")
	}
}
