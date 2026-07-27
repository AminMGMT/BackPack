package webui

import (
	"testing"

	"github.com/backpack/backpack/internal/localproxy"
)

// The built-in proxy is off by default, and a panel field for something nobody
// turned on is noise. More than noise, in this case: reporting it would mean
// asking systemd about a unit that was never installed on every stats poll.

func TestProxyIsAbsentFromStatsWhenItWasNeverEnabled(t *testing.T) {
	restore := useTempProxyDir(t) // no proxy.json here, so nothing is configured
	defer restore()

	s := GatherSystem()

	if s.ProxyEnabled {
		t.Error("the proxy reads as enabled with no configuration on disk")
	}
	if s.ProxyRunning {
		t.Error("the proxy reads as running when it was never enabled")
	}
	if s.ProxyType != "" || s.ProxyPort != 0 {
		t.Errorf("proxy details leaked while disabled: type=%q port=%d — the panel would draw a tile for it",
			s.ProxyType, s.ProxyPort)
	}
}

// Once it is on, the panel needs enough to say which proxy on which port, and
// it needs the two states kept apart: a tunnel forwarding a port to a proxy
// that is not running looks healthy everywhere else while refusing every
// connection, which is the whole reason for showing it.
func TestEnabledProxyReportsWhatItIsAndWhetherItAnswers(t *testing.T) {
	restore := useTempProxyDir(t)
	defer restore()

	if err := localproxy.Save(localproxy.Config{
		Enabled: true, Type: localproxy.SOCKS5, Port: 10800,
	}); err != nil {
		t.Fatalf("saving the proxy config: %v", err)
	}

	s := GatherSystem()

	if !s.ProxyEnabled {
		t.Fatal("an enabled proxy is not reported as enabled")
	}
	if s.ProxyType != string(localproxy.SOCKS5) {
		t.Errorf("proxy type = %q, want %q", s.ProxyType, localproxy.SOCKS5)
	}
	if s.ProxyPort != 10800 {
		t.Errorf("proxy port = %d, want 10800", s.ProxyPort)
	}
	// No unit is installed in a test environment, so "enabled but not running"
	// is exactly the disagreement the panel exists to show.
	if s.ProxyRunning {
		t.Error("the proxy reports as running with no service installed")
	}
}

// The running version has to reach the panel, or the update banner can only say
// where to go and not what is being left behind.
func TestVersionIsReported(t *testing.T) {
	if s := GatherSystem(); s.Version == "" {
		t.Error("no version reported; the update notice cannot say what is running")
	}
}

// useTempProxyDir points the proxy config at an empty directory and puts the
// real one back afterwards. Restoring the previous value rather than clearing
// it matters: the package variable is global, so a test that leaves it blank
// silently changes where every later test looks.
func useTempProxyDir(t *testing.T) func() {
	t.Helper()
	previous := localproxy.Dir
	localproxy.Dir = t.TempDir()
	return func() { localproxy.Dir = previous }
}
