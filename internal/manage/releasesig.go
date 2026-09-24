package manage

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// Proving a release came from the person who publishes them.
//
// Every release carries a SHA256SUMS file, and the updater refuses to install
// an archive whose hash is not in it. That is what stops a mirror handing over
// a different binary, and it is most of the protection — but it is not all of
// it, because the list travels the same channel as the thing it describes. A
// party that can substitute the archive on one of the third-party proxies these
// machines are obliged to use can substitute the list beside it, and both then
// agree with each other.
//
// A signature does not travel that channel. It is checked against a key that
// came with the binary already running on the machine, which arrived when
// somebody installed it and has not been near a mirror since.
//
// The signature is over SHA256SUMS rather than over each archive. One file
// covers every asset, because every asset is already verified against it, and
// the signing step in CI is then one line over one file instead of a loop that
// can miss an architecture — which is how five of seven architectures once came
// to be built and not published.

// sigAssetName is the signature published beside the checksum list.
const sigAssetName = "SHA256SUMS.sig"

// releasesAreSigned reports whether this build has a key to check against.
//
// False is what every build before signing existed does, and it is not a
// failure: the checksum is still mandatory. It is said out loud once per update
// so nobody reads "checksum verified" as more than it is.
func releasesAreSigned() bool { return strings.TrimSpace(app.ReleasePublicKey) != "" }

// verifyChecksumSignature checks the signature over a release's SHA256SUMS.
//
// It returns nil when this build pins no key, so an older release published
// before signing began still installs. Once a key is pinned, a missing
// signature is a refusal rather than a warning: a release that cannot be
// checked is exactly the case this exists for, and "warn and install anyway" is
// the same as not checking.
func verifyChecksumSignature(tag string, sums []byte) error {
	if !releasesAreSigned() {
		return nil
	}
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(app.ReleasePublicKey))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		// A build whose pinned key is malformed cannot verify anything, and
		// installing on the strength of a key that does not parse would be
		// pretending to. This is a build fault, so it says so.
		return fmt.Errorf("this build's release key is not a valid Ed25519 public key")
	}

	sig, err := fetchReleaseAsset(tag, sigAssetName)
	if err != nil {
		return fmt.Errorf("release %s publishes no signature for its checksums, and this "+
			"build requires one: %w\nInstall offline instead — see the README", tag, err)
	}
	return checkReleaseSignature(pub, tag, sums, sig)
}

// checkReleaseSignature verifies a published signature for one tag's checksum
// list. Separate from the fetching so the signing tool's output can be checked
// against it directly.
func checkReleaseSignature(pub []byte, tag string, sums, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil {
		return fmt.Errorf("the signature published for %s is not readable", tag)
	}
	// Over the tag as well as the list: see app.ReleaseSignedMessage for the
	// downgrade a signature over the list alone allowed.
	if !ed25519.Verify(ed25519.PublicKey(pub), app.ReleaseSignedMessage(tag, sums), raw) {
		return fmt.Errorf("the checksums published for %s are not signed for %s by the key "+
			"this build trusts — the release has been altered, it was published by somebody "+
			"else, or it is another release's files served under this tag", tag, tag)
	}
	return nil
}

// fetchReleaseAsset downloads one small file from a release, through the same
// sources the rest of the updater uses.
func fetchReleaseAsset(tag, name string) ([]byte, error) {
	url := fmt.Sprintf("%s/releases/download/%s/%s", repoURL(), tag, name)

	var lastErr error = fmt.Errorf("no source reachable")
	for _, s := range sources(30 * time.Second) {
		resp, err := s.client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("%s returned status %d", s.name, resp.StatusCode)
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return body, nil
	}
	return nil, lastErr
}
