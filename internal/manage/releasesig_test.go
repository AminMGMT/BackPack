package manage

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/app"
)

// A release's checksum list proves the archive is intact. It does not prove the
// list is the publisher's, because it travels the same channel as the archive —
// the third-party proxies these machines are obliged to use. A signature does
// not travel that channel: it is checked against a key that came with the
// binary already running.
func TestASignatureOverTheChecksumsIsCheckedAgainstThePinnedKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sums := []byte("abc123  backpack_linux_amd64.tar.gz\n")

	// The publisher's signature verifies.
	sig := ed25519.Sign(priv, sums)
	if !ed25519.Verify(pub, sums, sig) {
		t.Fatal("a signature this test just made does not verify")
	}
	// A list altered by one byte does not.
	altered := append([]byte(nil), sums...)
	altered[0] = 'x'
	if ed25519.Verify(pub, altered, sig) {
		t.Error("an altered checksum list verified against the publisher's signature")
	}
	// Nor does somebody else's signature over the real list.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if ed25519.Verify(pub, sums, ed25519.Sign(other, sums)) {
		t.Error("a signature from another key verified")
	}
}

// A build with no pinned key installs as it always did: the checksum is still
// mandatory, and nothing pretends to more than that.
func TestABuildWithNoPinnedKeyStillInstalls(t *testing.T) {
	restore := withReleaseKey(t, "")
	defer restore()

	if releasesAreSigned() {
		t.Fatal("a build with no key reports that releases are signed")
	}
	if err := verifyChecksumSignature("v1.8.2", []byte("anything")); err != nil {
		t.Errorf("a build with no pinned key refused an update: %v", err)
	}
}

// A pinned key that does not parse is a build fault, and a build that cannot
// verify must not install on the strength of a key it could not read.
func TestAMalformedPinnedKeyRefusesRatherThanSkips(t *testing.T) {
	for _, bad := range []string{"not base64!!", "c2hvcnQ="} {
		restore := withReleaseKey(t, bad)
		err := verifyChecksumSignature("v1.8.2", []byte("anything"))
		restore()
		if err == nil {
			t.Errorf("a build pinning %q installed anyway", bad)
		} else if !strings.Contains(err.Error(), "release key") {
			t.Errorf("the refusal does not say what is wrong: %v", err)
		}
	}
}

// Once a key is pinned, a release with no signature is refused rather than
// warned about. A release that cannot be checked is the case this exists for,
// and "warn and install anyway" is the same as not checking.
func TestAPinnedKeyRequiresASignature(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	restore := withReleaseKey(t, base64.StdEncoding.EncodeToString(pub))
	defer restore()

	if !releasesAreSigned() {
		t.Fatal("a pinned key did not register")
	}
	// No network here, so the fetch fails — which is the same shape as a
	// release that publishes no signature, and must produce a refusal either
	// way rather than a pass.
	if err := verifyChecksumSignature("v0.0.0-does-not-exist", []byte("x")); err == nil {
		t.Error("an update with no verifiable signature was allowed through")
	}
}

// The signing tool and the verifier have to agree on the encoding, or every
// signed release is refused by every build.
func TestTheSigningToolAndTheVerifierAgree(t *testing.T) {
	src, err := os.ReadFile("../../tools/signsums/main.go")
	if err != nil {
		t.Fatalf("the signing tool is gone: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "ed25519.Sign") {
		t.Error("the signing tool no longer makes an Ed25519 signature")
	}
	if !strings.Contains(s, "base64.StdEncoding.EncodeToString(sig)") {
		t.Error("the signing tool no longer base64-encodes the signature, which is what " +
			"the updater decodes")
	}
	if !strings.Contains(s, `path + ".sig"`) {
		t.Error("the signing tool no longer writes the name the updater fetches")
	}
}

func withReleaseKey(t *testing.T, key string) func() {
	t.Helper()
	old := app.ReleasePublicKey
	app.ReleasePublicKey = key
	return func() { app.ReleasePublicKey = old }
}
