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
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: signsums <path to SHA256SUMS>")
		os.Exit(2)
	}
	path := os.Args[1]

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
	sig := ed25519.Sign(ed25519.PrivateKey(key), sums)
	out := path + ".sig"
	if err := os.WriteFile(out, []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Signed:", out)
}
