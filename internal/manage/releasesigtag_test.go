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

	cmd := signsums(sumsPath, priv, pub, "RELEASE_TAG=v9.9.9")
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

// signsums is the signing tool as the release workflow runs it, with this
// test's key pair and the given environment on top. What the runner's own
// GITHUB_REF_* say is cleared first, so a test run by CI on a branch sees
// only what the test sets.
func signsums(sumsPath string, priv ed25519.PrivateKey, pub ed25519.PublicKey, env ...string) *exec.Cmd {
	cmd := exec.Command("go", "run", "./tools/signsums", sumsPath)
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(),
		"RELEASE_SIGNING_KEY="+base64.StdEncoding.EncodeToString(priv),
		"RELEASE_PUBLIC_KEY="+base64.StdEncoding.EncodeToString(pub),
		"RELEASE_TAG=", "GITHUB_REF_NAME=", "GITHUB_REF_TYPE=")
	cmd.Env = append(cmd.Env, env...)
	return cmd
}

// v2.0.0 was published from a manual run on main, and the signing tool took
// the tag from GITHUB_REF_NAME — which on such a run is the branch. Its
// checksums were signed for "main", and every updater refused the release.
func TestAManualReleaseRunIsSignedForItsTagNotItsBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the signing tool")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	version, err := os.ReadFile("../../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	tag := strings.TrimSpace(string(version))
	sums := []byte("0123abcd  backpack_linux_amd64.tar.gz\n")
	sign := func(t *testing.T, env ...string) ([]byte, string, error) {
		t.Helper()
		sumsPath := filepath.Join(t.TempDir(), "SHA256SUMS")
		if err := os.WriteFile(sumsPath, sums, 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := signsums(sumsPath, priv, pub, env...).CombinedOutput()
		sig, _ := os.ReadFile(sumsPath + ".sig")
		return sig, string(out), err
	}

	t.Run("the workflow's tag", func(t *testing.T) {
		sig, out, err := sign(t, "RELEASE_TAG=v9.9.9", "GITHUB_REF_TYPE=branch", "GITHUB_REF_NAME=main")
		if err != nil {
			t.Fatalf("signsums: %v\n%s", err, out)
		}
		if err := checkReleaseSignature(pub, "v9.9.9", sums, sig); err != nil {
			t.Fatalf("signed with RELEASE_TAG=v9.9.9 on main, it does not verify for v9.9.9: %v", err)
		}
	})
	t.Run("a branch is never the tag", func(t *testing.T) {
		sig, out, err := sign(t, "GITHUB_REF_TYPE=branch", "GITHUB_REF_NAME=main")
		if err != nil {
			t.Fatalf("signsums: %v\n%s", err, out)
		}
		if checkReleaseSignature(pub, "main", sums, sig) == nil {
			t.Fatal("a manual run on main was signed for \"main\" again")
		}
		if err := checkReleaseSignature(pub, tag, sums, sig); err != nil {
			t.Fatalf("a manual run on main is not signed for VERSION's %s: %v", tag, err)
		}
	})
	t.Run("a pushed tag", func(t *testing.T) {
		sig, out, err := sign(t, "GITHUB_REF_TYPE=tag", "GITHUB_REF_NAME=v8.8.8")
		if err != nil {
			t.Fatalf("signsums: %v\n%s", err, out)
		}
		if err := checkReleaseSignature(pub, "v8.8.8", sums, sig); err != nil {
			t.Fatalf("a pushed tag's run is not signed for it: %v", err)
		}
	})
	t.Run("anything but a release tag is refused", func(t *testing.T) {
		if _, out, err := sign(t, "RELEASE_TAG=main"); err == nil {
			t.Fatalf("signsums signed for \"main\": %s", out)
		}
	})
	t.Run("a key builds do not trust is refused", func(t *testing.T) {
		other, _, _ := ed25519.GenerateKey(rand.Reader)
		if _, out, err := sign(t, "RELEASE_TAG=v9.9.9",
			"RELEASE_PUBLIC_KEY="+base64.StdEncoding.EncodeToString(other)); err == nil {
			t.Fatalf("signsums signed with a key no updater trusts: %s", out)
		}
	})
}

// The release workflow hands the signing step the tag it resolved. Without it
// the tool falls back to what the runner says, which is how v2.0.0 came to be
// signed for "main".
func TestTheReleaseWorkflowSignsForTheTagItResolved(t *testing.T) {
	b, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "run: make release")
	if i < 0 {
		t.Fatal("the release workflow no longer runs make release")
	}
	step := s[strings.LastIndex(s[:i], "- name:"):i]
	if !strings.Contains(step, "RELEASE_TAG: ${{ steps.tag.outputs.tag }}") {
		t.Fatalf("the build step does not pass the resolved tag to the signing tool:\n%s", step)
	}
}
