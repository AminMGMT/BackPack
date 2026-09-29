package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A registry that cannot be read is not an empty fleet to be written over.
//
// It used to be: the file was parsed with the error ignored, so a file that
// did not parse — a hand edit with a stray comma, a disk that filled halfway
// through something else — came back as no servers at all, and the next change
// to the fleet saved that emptiness over it. Every server's address, login and
// sealed password went with it, silently.
func TestAnUnreadableRegistryIsKeptAsideNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	defer pointStoreAt(t, dir)()

	broken := []byte(`{"nodes": [{"name": "de1", "host": "203.0.113.7",` /* cut short */)
	if err := os.WriteFile(StorePath, broken, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Add("fi1", "198.51.100.4", 22, "root", "pw"); err != nil {
		t.Fatalf("adding a server: %v", err)
	}

	kept, _ := filepath.Glob(StorePath + ".unreadable-*")
	if len(kept) != 1 {
		t.Fatalf("the unreadable registry was not kept aside: %v", kept)
	}
	got, err := os.ReadFile(kept[0])
	if err != nil || string(got) != string(broken) {
		t.Fatalf("what was kept is not what was there: %q, %v", got, err)
	}
	if s := LoadStore(); len(s.Nodes) != 1 || !strings.EqualFold(s.Nodes[0].Name, "fi1") {
		t.Fatalf("the registry after the add = %+v", s.Nodes)
	}
}
