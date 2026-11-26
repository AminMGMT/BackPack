// Command signsums signs a release's SHA256SUMS, for the release workflow.
//
// It reads the private key from RELEASE_SIGNING_KEY — base64 of the 64-byte
// Ed25519 private key that `make release-key` printed — and writes a detached
// signature beside the file. The signature is base64 of the raw 64 bytes, which
// is what the updater expects; see internal/manage/releasesig.go.
//
// With no key in the environment it does nothing and says so, so a fork or a
// local `make release` still produces a full set of assets.
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/backpack/backpack/internal/app"
)

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fmt.Fprintln(os.Stderr, "usage: signsums <path to SHA256SUMS> [tag]")
		os.Exit(2)
	}
	path := os.Args[1]
	// The tag is signed with the list: see app.ReleaseSignedMessage. On CI it
	// is the tag being released; by hand, the one given, or the VERSION file.
	tag := releaseTag()
	if len(os.Args) == 3 {
		tag = os.Args[2]
	}
	if tag == "" {
		fmt.Fprintln(os.Stderr, "no release tag: set RELEASE_TAG, pass one, or run from the repository root")
		os.Exit(1)
	}
	// A signature is good for exactly the tag it names, so one made for
	// anything but a release tag installs nowhere. v2.0.0 was published from
	// a manual run on main and signed for "main"; every updater refused it.
	if !releaseTagPattern.MatchString(tag) {
		fmt.Fprintf(os.Stderr, "%q is not a release tag (vX.Y.Z or vX.Y.Z-suffix); a signature for it would install nowhere\n", tag)
		os.Exit(1)
	}

	keyB64 := strings.TrimSpace(os.Getenv("RELEASE_SIGNING_KEY"))
	if keyB64 == "" {
		fmt.Println("RELEASE_SIGNING_KEY is not set — publishing without a signature.")
		return
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(key) != ed25519.PrivateKeySize {
		fmt.Fprintln(os.Stderr, "RELEASE_SIGNING_KEY is not a base64 Ed25519 private key")
		os.Exit(1)
	}

	sums, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sig := ed25519.Sign(ed25519.PrivateKey(key), app.ReleaseSignedMessage(tag, sums))
	// The key builds check against, and the key this signed with, must be one
	// pair: a secret that does not match is a release no updater installs,
	// and it is far cheaper to fail here than after publishing.
	if err := checkTrusted(ed25519.PrivateKey(key), tag, sums, sig); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out := path + ".sig"
	if err := os.WriteFile(out, []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Signed for", tag+":", out)
}

// releaseTagPattern is what the release workflow accepts as a tag.
var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

// releaseTag is the tag being released: RELEASE_TAG, which the release
// workflow sets to the tag it resolved; GITHUB_REF_NAME when the run was
// started by pushing a tag; and otherwise the VERSION file.
//
// GITHUB_REF_NAME alone is not enough. A run started by hand on a branch has
// the branch's name there ("main"), not a tag, and that is what v2.0.0 was
// signed for.
func releaseTag() string {
	if t := strings.TrimSpace(os.Getenv("RELEASE_TAG")); t != "" {
		return t
	}
	if os.Getenv("GITHUB_REF_TYPE") == "tag" {
		if t := strings.TrimSpace(os.Getenv("GITHUB_REF_NAME")); t != "" {
			return t
		}
	}
	v, err := os.ReadFile("VERSION")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(v))
}

// checkTrusted verifies the signature just made the way an updater will:
// against the public key builds carry, for this tag. RELEASE_PUBLIC_KEY stands
// in for that key where a test signs with one of its own.
func checkTrusted(priv ed25519.PrivateKey, tag string, sums, sig []byte) error {
	trusted := strings.TrimSpace(os.Getenv("RELEASE_PUBLIC_KEY"))
	if trusted == "" {
		trusted = strings.TrimSpace(app.ReleasePublicKey)
	}
	pub, err := base64.StdEncoding.DecodeString(trusted)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("the release public key builds trust is not a valid Ed25519 key")
	}
	if !bytes.Equal(pub, priv.Public().(ed25519.PublicKey)) {
		return fmt.Errorf("RELEASE_SIGNING_KEY is not the private half of the key builds trust " +
			"(app.ReleasePublicKey); every updater would refuse this release")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), app.ReleaseSignedMessage(tag, sums), sig) {
		return fmt.Errorf("the signature does not verify for %s", tag)
	}
	return nil
}
