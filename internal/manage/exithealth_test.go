package manage

import (
	"strings"
	"testing"
)

// health_failover must survive being written to the config, or the client would
// never see it and automatic failover would silently never start.
func TestHealthFailoverWrittenToConfig(t *testing.T) {
	s := TunnelSpec{
		Role:           "client",
		Transport:      "kcp",
		RemoteAddr:     "1.1.1.1:443",
		FallbackAddrs:  []string{"2.2.2.2:443"},
		HealthFailover: true,
	}
	if out := s.Render(); !strings.Contains(out, "health_failover = true") {
		t.Fatalf("config is missing health_failover:\n%s", out)
	}
}

// reorderPrimary promotes a backup to primary and demotes the old primary into
// the fallback list without losing any address or duplicating the new one.
func TestReorderPrimaryLosesNothing(t *testing.T) {
	primary, backups := reorderPrimary("a:443", []string{"b:443", "c:443"}, "c:443")
	if primary != "c:443" {
		t.Fatalf("primary = %q, want c:443", primary)
	}
	if got := strings.Join(backups, ","); got != "a:443,b:443" {
		t.Fatalf("backups = %q, want a:443,b:443", got)
	}
}
