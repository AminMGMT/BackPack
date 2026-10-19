package manage

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/metrics"
)

// A reset stops a running tunnel before it touches the count — the engine
// would otherwise write its in-memory total straight back — moves what it had
// carried to the server's ledger, and starts it again (#106).
func TestResetUsageZeroesTheCountAndKeepsTheServerTotal(t *testing.T) {
	dir := withResetFixture(t)
	writeMetrics(t, dir, "iran-443", `{"name":"iran-443","bytes_in":700,"bytes_out":300}`)

	var calls []string
	IsActive = func(string) bool { return true }
	StopService = func(s string) error {
		calls = append(calls, "stop "+s)
		return nil
	}
	StartService = func(s string) error {
		// The count has to be gone before the engine starts and reads it.
		if _, err := os.Stat(metrics.Path(dir, "iran-443")); !os.IsNotExist(err) {
			t.Errorf("the tunnel started while its old count was still on disk: %v", err)
		}
		calls = append(calls, "start "+s)
		return nil
	}

	if err := resetUsageIn(dir, "iran-443"); err != nil {
		t.Fatal(err)
	}
	service := app.ServiceName("iran-443")
	if strings.Join(calls, ", ") != "stop "+service+", start "+service {
		t.Errorf("service calls %q", calls)
	}
	if in, out := metrics.Retired(dir); in != 700 || out != 300 {
		t.Errorf("the server's ledger holds %d in / %d out, want 700 / 300", in, out)
	}
}

// A stopped tunnel stays stopped, and a failure on one tunnel does not stop
// the rest from being reset.
func TestResetAllUsageGoesOnPastAFailure(t *testing.T) {
	dir := withResetFixture(t)
	for _, name := range []string{"a", "b"} {
		writeMetrics(t, dir, name, `{"bytes_in":5,"bytes_out":5}`)
	}
	IsActive = func(s string) bool { return s == app.ServiceName("a") }
	StopService = func(string) error { return errors.New("no systemd") }
	StartService = func(s string) error {
		t.Errorf("started %s, which was not running", s)
		return nil
	}

	reset, err := resetAllIn(dir, []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "a: ") {
		t.Errorf("the failure on a was not reported: %v", err)
	}
	if len(reset) != 1 || reset[0] != "b" {
		t.Errorf("reset %q, want [b]", reset)
	}
	if _, err := os.Stat(metrics.Path(dir, "a")); err != nil {
		t.Errorf("a's count went although its tunnel could not be stopped: %v", err)
	}
	if _, err := os.Stat(metrics.Path(dir, "b")); !os.IsNotExist(err) {
		t.Errorf("b's count is still there: %v", err)
	}
}

func withResetFixture(t *testing.T) string {
	t.Helper()
	prevActive, prevStop, prevStart := IsActive, StopService, StartService
	t.Cleanup(func() { IsActive, StopService, StartService = prevActive, prevStop, prevStart })
	return t.TempDir()
}

func writeMetrics(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(metrics.Path(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
