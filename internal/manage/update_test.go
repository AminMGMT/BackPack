package manage

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

const sumsFile = `b6d6e94459a69ff58f9bf5219c802673459edf41e411e01d81b0bf8c42835607  backpack_linux_amd64.tar.gz
19bb7c77150be971a798bf6b4f63f638f24b33eb60605e70ccd6f776431cf6ea  backpack_linux_arm64.tar.gz
`

func TestHashForPicksTheRightAsset(t *testing.T) {
	got := hashFor(sumsFile, "backpack_linux_arm64.tar.gz")
	want := "19bb7c77150be971a798bf6b4f63f638f24b33eb60605e70ccd6f776431cf6ea"
	if got != want {
		t.Fatalf("hashFor = %q, want %q", got, want)
	}
}

func TestHashForUnknownAsset(t *testing.T) {
	// An asset with no published checksum must yield an empty string, not the
	// hash of some other file — installing the wrong binary would be worse
	// than installing an unverified one.
	if got := hashFor(sumsFile, "backpack_linux_riscv.tar.gz"); got != "" {
		t.Fatalf("hashFor for an unlisted asset = %q, want empty", got)
	}
}

func TestHashForBinaryModeMarker(t *testing.T) {
	// sha256sum writes "hash *name" in binary mode; both forms must parse.
	sums := "abc123  plain.tar.gz\ndef456 *binary.tar.gz\n"
	if got := hashFor(sums, "binary.tar.gz"); got != "def456" {
		t.Fatalf("hashFor with a binary-mode marker = %q, want def456", got)
	}
}

func TestHashForIgnoresJunkLines(t *testing.T) {
	sums := "# a comment\n\nnot-a-valid-line\nabc123  real.tar.gz\n"
	if got := hashFor(sums, "real.tar.gz"); got != "abc123" {
		t.Fatalf("hashFor = %q, want abc123", got)
	}
}

func TestVerifyChecksumAcceptsAndRejects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "asset.tar.gz")
	content := []byte("pretend this is a release archive")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	good := hex.EncodeToString(sum[:])

	if err := verifyChecksum(path, good); err != nil {
		t.Fatalf("a matching checksum was rejected: %v", err)
	}
	// Case must not matter — checksum files in the wild use both.
	if err := verifyChecksum(path, "ABCDEF"+good[6:]); err == nil {
		t.Fatal("a wrong checksum was accepted")
	}

	// The case that actually protects the user: the file changed in transit.
	if err := os.WriteFile(path, append(content, 'x'), 0644); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(path, good); err == nil {
		t.Fatal("a tampered file passed verification")
	}
}
