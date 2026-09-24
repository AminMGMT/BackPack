package manage

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The signing tool, run as CI runs it, and the updater's check, agree — and a
// signature made for one tag does not pass for another. Before the tag was
// signed, an older release's genuine files could be served under a newer tag
// and would verify: a downgrade with the publisher's own signature on it.
func TestAReleaseSignatureIsGoodForItsOwnTagOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the signing tool")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	sumsPath := filepath.Join(dir, "SHA256SUMS")
	sums := []byte("0123abcd  backpack_linux_amd64.tar.gz\n")
	if err := os.WriteFile(sumsPath, sums, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "run", "./tools/signsums", sumsPath)
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(),
		"RELEASE_SIGNING_KEY="+base64.StdEncoding.EncodeToString(priv),
		"GITHUB_REF_NAME=v9.9.9")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("signsums: %v\n%s", err, out)
	} else if !strings.Contains(string(out), "v9.9.9") {
		t.Fatalf("signsums did not say which tag it signed for: %s", out)
	}
	sig, err := os.ReadFile(sumsPath + ".sig")
	if err != nil {
		t.Fatal(err)
	}

	if err := checkReleaseSignature(pub, "v9.9.9", sums, sig); err != nil {
		t.Fatalf("the tool's signature does not verify for its own tag: %v", err)
	}
	if err := checkReleaseSignature(pub, "v10.0.0", sums, sig); err == nil {
		t.Fatal("a signature for v9.9.9 verified for v10.0.0 — the downgrade is open")
	}
	altered := append([]byte(nil), sums...)
	altered[0] = 'f'
	if err := checkReleaseSignature(pub, "v9.9.9", altered, sig); err == nil {
		t.Fatal("an altered checksum list verified")
	}
}
