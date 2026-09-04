package webui

import (
	"os"
	"strings"
	"testing"
)

// readServerSource reads the route table, so a test can check what a route is
// wrapped in rather than what it is named.
func readServerSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("reading the route table: %v", err)
	}
	return string(b)
}

// Both endpoints change something or reveal where a backend lives, so neither
// belongs to the read-only remote token.
func TestTheSpeedTestEndpointsNeedWriteAuth(t *testing.T) {
	src := readServerSource(t)

	for _, route := range []string{"/api/speedtest/plan", "/api/speedtest"} {
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
