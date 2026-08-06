package instanceid

import (
	"path/filepath"
	"testing"
)

func TestDeterministicIdentityIsStableAndNonZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.toml")
	a, b := Deterministic(path), Deterministic(path)
	if a != b || a.InstanceID == "" || a.Connmark == 0 {
		t.Fatalf("unstable deterministic identity: %#v %#v", a, b)
	}
}

func TestCreatePersistsRandomManagedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "direct.toml")
	id, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if id.InstanceID == "" || id.Connmark == 0 || id == Deterministic(path) {
		t.Fatalf("managed identity was not random and complete: %#v", id)
	}
	again, err := Create(path)
	if err != nil || again != id {
		t.Fatalf("managed identity was not persisted: %#v, %v", again, err)
	}
}
