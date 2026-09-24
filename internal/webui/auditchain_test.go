package webui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/alerthist"
)

func writeSome(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		record(auditEntry{At: int64(1000 + i), Who: "the panel", Method: "POST", Path: "/api/tunnel/action", Status: 200})
	}
}

func rewrite(t *testing.T, change func([]auditEntry) []auditEntry) {
	t.Helper()
	all := readAudit()
	data, _ := json.Marshal(change(all))
	if err := os.WriteFile(AuditPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheRecordIsAnIntactChain(t *testing.T) {
	isolateAccess(t)
	isolateAlerts(t)
	writeSome(t, 5)
	if intact, at, head := AuditIntegrity(); !intact || at != -1 || head == "" {
		t.Fatalf("a record nobody touched reads as intact=%v brokenAt=%d head=%q", intact, at, head)
	}
}

func TestEditingALineIsNoticed(t *testing.T) {
	isolateAccess(t)
	isolateAlerts(t)
	writeSome(t, 5)
	rewrite(t, func(a []auditEntry) []auditEntry { a[2].Who = "somebody else"; return a })
	if intact, at, _ := AuditIntegrity(); intact || at != 2 {
		t.Fatalf("an edited line: intact=%v brokenAt=%d, want broken at 2 from the top", intact, at)
	}
}

func TestDeletingALineIsNoticed(t *testing.T) {
	isolateAccess(t)
	isolateAlerts(t)
	writeSome(t, 5)
	rewrite(t, func(a []auditEntry) []auditEntry { return append(a[:2], a[3:]...) })
	if intact, _, _ := AuditIntegrity(); intact {
		t.Fatal("a deleted line went unnoticed")
	}
}

// Rewriting the whole chain consistently is possible for root — and changes
// the head, which is what the forwarded copies are compared against.
func TestRecomputingTheChainChangesTheHead(t *testing.T) {
	isolateAccess(t)
	isolateAlerts(t)
	writeSome(t, 3)
	_, _, before := AuditIntegrity()
	rewrite(t, func(a []auditEntry) []auditEntry {
		var out []auditEntry
		for i, e := range a {
			if i == 1 {
				continue
			}
			e.Prev, e.Hash = "", ""
			out = append(out, chainAudit(out, e))
		}
		return out
	})
	intact, _, after := AuditIntegrity()
	if !intact || after == before {
		t.Fatalf("intact=%v head %s → %s; a consistent rewrite must still move the head", intact, before, after)
	}
}

// The cap drops the oldest entries; that must not read as tampering.
func TestTheCapDoesNotBreakTheChain(t *testing.T) {
	isolateAccess(t)
	isolateAlerts(t)
	old := auditKeep
	auditKeep = 4
	t.Cleanup(func() { auditKeep = old })
	writeSome(t, 10)
	if intact, at, _ := AuditIntegrity(); !intact {
		t.Fatalf("the cap broke the chain at %d", at)
	}
}

// Lines from before the chain existed are not tampering.
func TestUnchainedOldLinesAreTolerated(t *testing.T) {
	isolateAccess(t)
	isolateAlerts(t)
	data, _ := json.Marshal([]auditEntry{{At: 1, Who: "the panel", Method: "POST", Path: "/api/x"}})
	os.WriteFile(AuditPath, data, 0o600)
	writeSome(t, 3)
	if intact, at, _ := AuditIntegrity(); !intact {
		t.Fatalf("a pre-chain line counted as tampering at %d", at)
	}
}

// What leaves the machine names the head, so a later rewrite can be caught
// against it.
func TestAForwardedLineCarriesTheHead(t *testing.T) {
	isolateAccess(t)
	isolateAlerts(t)
	record(auditEntry{At: 1, Who: "token ci", Method: "POST", Path: "/api/password", Status: 200})
	_, _, head := AuditIntegrity()
	events := alerthist.Load().Events
	if len(events) == 0 || !strings.Contains(events[len(events)-1].Message, "#"+shortHash(head)) {
		t.Fatalf("the forwarded line does not carry the head #%s: %+v", shortHash(head), events)
	}
}
