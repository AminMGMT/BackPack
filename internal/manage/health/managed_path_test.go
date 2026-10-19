package health

import (
	"testing"

	"github.com/backpack/backpack/internal/manage/core"
	"github.com/backpack/backpack/internal/metrics"
)

func TestManagedPoolDiagnosisUsesEngineEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		pool *metrics.PoolStats
		want CheckLevel
	}{
		{"server-or-unavailable", nil, CheckInfo},
		{"healthy-client", &metrics.PoolStats{Live: 8, Target: 8, Configured: 8}, CheckOK},
		{"empty-client-pool", &metrics.PoolStats{Target: 8, Configured: 8}, CheckWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if c := managedPoolCheck("test", tc.pool); c.Level != tc.want {
				t.Fatalf("pool diagnosis = %+v, want %v", c, tc.want)
			}
		})
	}
}

func TestManagedPathDoesNotDiagnoseTheInnerLoopbackAsTheHTTPSPath(t *testing.T) {
	for _, role := range []string{"server", "client"} {
		checks := managedPathChecks("test", core.Tunnel{Name: "managed-no-snapshot-fixture", Role: role, Transport: "tcp", Addr: "127.0.0.1:62101"})
		if len(checks) != 2 {
			t.Fatalf("unexpected managed path checks: %+v", checks)
		}
		for _, c := range checks {
			if c.Level != CheckInfo {
				t.Fatalf("%s falsely diagnosed the loopback socket: %+v", role, c)
			}
		}
	}
}
