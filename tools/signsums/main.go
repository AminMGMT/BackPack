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
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"github.com/backpack/backpack/internal/app"
	"os"
	"strings"
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
		fmt.Fprintln(os.Stderr, "no release tag: set GITHUB_REF_NAME, pass one, or run from the repository root")
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
	out := path + ".sig"
	if err := os.WriteFile(out, []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Signed for", tag+":", out)
}

// releaseTag is the tag being released: CI's, or the VERSION file's.
func releaseTag() string {
	if t := strings.TrimSpace(os.Getenv("GITHUB_REF_NAME")); t != "" {
		return t
	}
	v, err := os.ReadFile("VERSION")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(v))
}
