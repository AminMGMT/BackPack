// Command releasekey generates the Ed25519 pair that release signatures use.
//
// Run it once, with `make release-key`. It prints two things and keeps neither:
//
//   - the public half, which goes into app.ReleasePublicKey and ships in every
//     binary from then on
//   - the private half, which goes into the repository's RELEASE_SIGNING_KEY
//     secret and must not go anywhere else
//
// Nothing is written to disk. A private key in a file beside the repository is
// a private key in somebody's next backup, and the only copy that should exist
// is the one in the secret store.
//
// Rotating is the same operation, and it is not free: a machine running an
// older binary trusts the old key, so it will refuse a release signed with the
// new one until it has been updated by some other route. Publish one release
// carrying the new public key while still signing with the old private key,
// then switch.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

func main() {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not generate a key:", err)
		os.Exit(1)
	}
	fmt.Println("Public key — paste into ReleasePublicKey in internal/app/app.go:")
	fmt.Println()
	fmt.Println("    " + base64.StdEncoding.EncodeToString(pub))
	fmt.Println()
	fmt.Println("Private key — store as the RELEASE_SIGNING_KEY repository secret,")
	fmt.Println("and nowhere else. It is not written to disk here.")
	fmt.Println()
	fmt.Println("    " + base64.StdEncoding.EncodeToString(priv))
	fmt.Println()
}
