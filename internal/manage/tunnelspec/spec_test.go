package tunnelspec

import (
	"fmt"
	"strings"
	"testing"
)

// The clamp only reaches the config file when it is set, so a tunnel that never
// had one does not grow an `mss = 0` line.
func TestClampIsWrittenOnlyWhenSet(t *testing.T) {
	render := func(s Spec) string {
		var b strings.Builder
		s.writeTuning(func(f string, a ...any) { b.WriteString(fmt.Sprintf(f, a...)) })
		return b.String()
	}
	if got := render(Spec{MSS: 1208}); !strings.Contains(got, "mss = 1208") {
		t.Fatalf("a set clamp was not written to the config: %q", got)
	}
	if got := render(Spec{}); strings.Contains(got, "mss") {
		t.Fatalf("an unset clamp was written anyway: %q", got)
	}
}
