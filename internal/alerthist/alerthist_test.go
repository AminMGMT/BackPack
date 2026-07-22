package alerthist

import (
	"fmt"
	"testing"
)

// A message must survive the round trip, and the active list must replace
// rather than accumulate.
func TestRecordAndLoad(t *testing.T) {
	Dir = t.TempDir()

	Record([]string{"⚠️ Processor at 91.0%"}, []string{"Processor above threshold"})
	st := Load()
	if len(st.Events) != 1 || st.Events[0].Message != "⚠️ Processor at 91.0%" {
		t.Fatalf("event not recorded: %+v", st.Events)
	}
	if len(st.Active) != 1 {
		t.Fatalf("active not recorded: %+v", st.Active)
	}

	Record([]string{"✅ Processor back to normal — 40.0%"}, nil)
	st = Load()
	if len(st.Events) != 2 {
		t.Fatalf("recovery not appended: %+v", st.Events)
	}
	if len(st.Active) != 0 {
		t.Fatalf("active should be cleared: %+v", st.Active)
	}
}

// A quiet pass — no messages, active unchanged — must not touch the file.
func TestQuietPassDoesNotRewrite(t *testing.T) {
	Dir = t.TempDir()
	Record([]string{"⚠️ Disk at 95.0%"}, []string{"Disk above threshold"})
	before := Load().Updated

	Record(nil, []string{"Disk above threshold"})
	if got := Load().Updated; !got.Equal(before) {
		t.Fatalf("quiet pass rewrote the file: %v -> %v", before, got)
	}
}

// The file must stay bounded no matter how long the monitor runs.
func TestEventsAreTrimmed(t *testing.T) {
	Dir = t.TempDir()
	for i := 0; i < maxEvents+20; i++ {
		Record([]string{fmt.Sprintf("event %d", i)}, nil)
	}
	st := Load()
	if len(st.Events) != maxEvents {
		t.Fatalf("expected %d events, got %d", maxEvents, len(st.Events))
	}
	if st.Events[len(st.Events)-1].Message != fmt.Sprintf("event %d", maxEvents+19) {
		t.Fatalf("newest event missing: %q", st.Events[len(st.Events)-1].Message)
	}
}
