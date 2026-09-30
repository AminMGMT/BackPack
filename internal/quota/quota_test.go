package quota

import "testing"

func TestAQuotaSurvivesAndClears(t *testing.T) {
	dir := t.TempDir()
	if q, err := Load(dir, "t1"); err != nil || q.Limit != 0 {
		t.Fatalf("no file should be no limit: %+v %v", q, err)
	}
	if err := Save(dir, "t1", 5<<40); err != nil {
		t.Fatal(err)
	}
	q, err := Load(dir, "t1")
	if err != nil || q.Limit != 5<<40 || q.Set.IsZero() {
		t.Fatalf("read back %+v %v", q, err)
	}
	if q.Reached(5<<40-1) || !q.Reached(5<<40) {
		t.Error("the limit is reached at the limit, not a byte before")
	}
	if err := Save(dir, "t1", 0); err != nil {
		t.Fatal(err)
	}
	if q, _ := Load(dir, "t1"); q.Limit != 0 || q.Reached(1<<50) {
		t.Error("zero is no limit")
	}
}
