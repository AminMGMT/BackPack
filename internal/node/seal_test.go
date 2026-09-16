package node

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The fleet registry holds the root password of a different machine, and it was
// written in plain text.
//
// Root-only and 0600, so not a vulnerability in the usual sense. What made it
// worth changing is where the file goes: the backup archive is the whole config
// directory, and a backup is a thing people move — downloaded through the
// panel, sent through the bot, kept on a laptop, attached to a support message.
// Every one of those carried the root password of every managed server in the
// clear.
func TestAPasswordIsNotWrittenInTheClear(t *testing.T) {
	isolateStore(t)
	const secret = "the-root-password-of-another-machine"

	if err := SaveStore(Store{Nodes: []Node{
		{Name: "hetzner", Host: "203.0.113.9", User: "root", Password: secret},
	}}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(StorePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Errorf("the password is on disk in the clear:\n%s", raw)
	}
	// And it is not simply missing — it has to come back.
	got := LoadStore()
	if len(got.Nodes) != 1 || got.Nodes[0].Password != secret {
		t.Fatalf("the password did not survive a save and load: %+v", got.Nodes)
	}
}

// Without the key the password is unreadable, which is the entire point: the
// key is the one file the backup does not carry.
func TestAPasswordCannotBeReadWithoutTheKey(t *testing.T) {
	isolateStore(t)
	const secret = "the-root-password-of-another-machine"
	if err := SaveStore(Store{Nodes: []Node{{Name: "a", Password: secret}}}); err != nil {
		t.Fatal(err)
	}

	// A copy of the registry on a machine with a different key — which is what
	// restoring a backup somewhere else is.
	if err := os.Remove(keyPath()); err != nil {
		t.Fatal(err)
	}

	got := LoadStore()
	if len(got.Nodes) != 1 {
		t.Fatalf("the fleet lost its servers along with its key: %+v", got.Nodes)
	}
	if got.Nodes[0].Password != "" {
		t.Error("the password was readable without the key it was sealed with")
	}
	// The server is still listed, and still has everything that is not secret:
	// a fleet that vanished would be worse than one that asks for a password.
	if got.Nodes[0].Name != "a" {
		t.Error("the server itself did not survive")
	}
}

// A registry written by a version that did not seal keeps working, and is
// sealed the next time it is saved.
func TestAPlaintextRegistryIsReadAndThenSealed(t *testing.T) {
	isolateStore(t)
	const secret = "written-by-an-older-version"

	legacy := `{"nodes":[{"name":"old","host":"198.51.100.7","user":"root","password":"` + secret + `"}]}`
	if err := os.WriteFile(StorePath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	got := LoadStore()
	if len(got.Nodes) != 1 || got.Nodes[0].Password != secret {
		t.Fatalf("an existing registry stopped working: %+v", got.Nodes)
	}

	if err := SaveStore(got); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(StorePath)
	if strings.Contains(string(raw), secret) {
		t.Errorf("saving did not seal the password that was already there:\n%s", raw)
	}
	if again := LoadStore(); again.Nodes[0].Password != secret {
		t.Error("the re-sealed password does not read back")
	}
}

// Nothing the panel hands out carries the password, sealed or otherwise.
func TestABlankedNodeCarriesNeitherForm(t *testing.T) {
	n := Node{Name: "a", Password: "secret", Sealed: "enc:v1:whatever", Fingerprint: "ab:cd"}
	b := blank(n)
	if b.Password != "" || b.Sealed != "" {
		t.Errorf("a blanked node still carries a credential: %+v", b)
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "enc:v1:") {
		t.Errorf("a blanked node marshals a credential: %s", data)
	}
}

// The key is the panel's alone.
func TestTheSealingKeyIsRootOnly(t *testing.T) {
	isolateStore(t)
	if _, err := seal("x"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(keyPath())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("the fleet key is %#o", st.Mode().Perm())
	}
}
