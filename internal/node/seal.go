package node

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/backpack/backpack/internal/app"
)

// Sealing the one credential this machine holds for somebody else.
//
// Everything else in the config directory is a secret about this machine: a
// tunnel's token, the panel's password, the bot's. The fleet registry is
// different — it holds the root password of a *different* server, and it holds
// it because the panel logs in over SSH to manage it.
//
// It was stored in plain text. Root-only and 0600, so not a vulnerability in
// the usual sense: anything that can read it can already read everything else
// here. What made it worth changing is where the file goes. The backup archive
// is the whole config directory, and a backup is a thing people move: it is
// downloaded through the panel, sent through the Telegram bot, kept on a
// laptop, attached to a support message. Every one of those carried the root
// password of every managed server, in the clear, to somewhere nobody was
// thinking about it.
//
// So the password is encrypted with a key that is not in the backup. That is
// the whole of the protection and it is worth being exact about it:
//
//   - Against root on this machine it is worth nothing. The key is beside the
//     data and has to be, because the panel needs the password without anybody
//     present to type one.
//   - Against a copy of the backup it is worth everything, which is the case
//     that actually happens.
//
// Restoring onto the machine that took the backup works, because the key
// survives: a restore seeds the staging tree from the live directory first, so
// a file the archive does not mention is kept. Restoring onto a *different*
// machine leaves the passwords unreadable, and the fleet screen asks for them
// again — which is the correct outcome, and the one an operator would choose if
// asked whether a backup should carry them.

// sealKeyName is the key file, beside the registry it seals.
//
// Derived from StorePath rather than kept as a second variable, so anything
// that points the registry somewhere else — a test, and there are several —
// takes the key with it. Two knobs meant every caller had to know about the
// second one, and the ones that did not tried to write the key into /etc while
// their registry was safely in a temp directory.
const sealKeyName = "node.key"

func keyPath() string { return filepath.Join(filepath.Dir(StorePath), sealKeyName) }

// sealPrefix marks a sealed value, so a plaintext password written by an older
// version is recognisable and can be read and then re-sealed.
const sealPrefix = "enc:v1:"

// sealingKey returns the key, creating it on first use.
func sealingKey() ([]byte, error) {
	if b, err := os.ReadFile(keyPath()); err == nil && len(b) == 32 {
		return b, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating the fleet key: %w", err)
	}
	// 0600 and never in the archive — see the note above and backup.go, which
	// skips it by name.
	if err := app.WriteFileAtomic(keyPath(), key, 0o600); err != nil {
		return nil, fmt.Errorf("writing the fleet key: %w", err)
	}
	return key, nil
}

// seal encrypts a password for storage. An empty password seals to nothing.
func seal(password string) (string, error) {
	if password == "" {
		return "", nil
	}
	gcm, err := sealingAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(password), nil)
	return sealPrefix + base64.StdEncoding.EncodeToString(out), nil
}

// unseal reverses seal. A value without the prefix is a password written by a
// version that did not seal them, and is returned as it is — which is what
// lets an existing registry keep working until its next save re-seals it.
//
// A sealed value that cannot be opened returns empty and no error. That is the
// registry having been restored onto a machine whose key is a different one,
// and it is not a failure to report to the caller: the fleet still lists its
// servers, and the one thing that cannot be done with them until the password
// is entered again is logging in.
func unseal(stored string) string {
	if stored == "" || !strings.HasPrefix(stored, sealPrefix) {
		return stored
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, sealPrefix))
	if err != nil {
		return ""
	}
	// Reading does not mint a key.
	//
	// It used to: unseal went through sealingAEAD, which creates one on first
	// use. That is right for sealing — you cannot write without a key — and it
	// is a trap on the read path, and precisely on the path that matters. A
	// registry restored onto a fresh machine has sealed values and no key; the
	// fleet screen reads them, a key is minted as a side effect of failing to
	// decrypt them, and ImportSealKey then refuses to install the real one
	// because "this machine already has a fleet key". The operator is locked
	// out of their own recovery by the act of looking at it.
	//
	// So a missing key here is what it has always meant one step later: this
	// value cannot be read, the fleet still lists its servers, and the password
	// has to be entered again.
	gcm, err := existingSealingAEAD()
	if err != nil {
		return ""
	}
	if len(raw) < gcm.NonceSize() {
		return ""
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return ""
	}
	return string(plain)
}

// existingSealingAEAD is sealingAEAD for the read path: it uses the key that is
// there and refuses rather than creating one. See unseal.
func existingSealingAEAD() (cipher.AEAD, error) {
	key, err := os.ReadFile(keyPath())
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("the fleet key is %d bytes rather than 32", len(key))
	}
	return aeadFor(key)
}

func sealingAEAD() (cipher.AEAD, error) {
	key, err := sealingKey()
	if err != nil {
		return nil, err
	}
	return aeadFor(key)
}

func aeadFor(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Taking the key deliberately.
//
// The seal is doing exactly what it was designed to do, and the design has a
// consequence nobody has hit yet: restoring a backup onto a different machine
// brings the fleet list back without the credentials to use it. That is correct
// for a backup that was emailed or left on a laptop. It is the wrong answer for
// the one case that matters most — the panel machine is gone and this is the
// recovery.
//
// So the key can be taken out and put back, on purpose, by somebody with a
// shell on the machine. Deliberately not through the panel and deliberately not
// in the archive: the whole protection is that the key and the data travel
// separately, and a button that put them back together would be the protection
// removed with a nicer name. Two things, two places, the operator's choice to
// bring them together.

// ExportSealKey returns the fleet key as a base64 string, for an operator who
// is keeping it somewhere other than this machine.
//
// It does not create one. A key that does not exist yet is a fleet with no
// sealed passwords in it, and handing out a freshly minted key would file
// something that protects nothing.
func ExportSealKey() (string, error) {
	b, err := os.ReadFile(keyPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("this machine has no fleet key yet — one is created the " +
				"first time a managed server's password is stored, so there is nothing to keep")
		}
		return "", fmt.Errorf("reading the fleet key: %w", err)
	}
	if len(b) != 32 {
		return "", fmt.Errorf("the fleet key is %d bytes rather than 32; it is damaged", len(b))
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// ImportSealKey puts a previously exported key back, so a restored registry
// becomes readable again.
//
// It refuses when a key is already present. Overwriting one would leave every
// password currently sealed on this machine unreadable, which is a worse
// outcome than the one being recovered from and is not undoable — the operator
// has to move the existing key out of the way themselves, having decided that
// is what they mean.
func ImportSealKey(encoded string) error {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return fmt.Errorf("that is not a fleet key: %w", err)
	}
	if len(key) != 32 {
		return fmt.Errorf("a fleet key is 32 bytes; this one is %d", len(key))
	}
	if b, err := os.ReadFile(keyPath()); err == nil && len(b) > 0 {
		if string(b) == string(key) {
			return nil // already the key we were given; nothing to do
		}
		return fmt.Errorf("this machine already has a fleet key, and replacing it would make "+
			"every password currently sealed here unreadable. Move %s aside first if that "+
			"is what you mean", keyPath())
	}
	if err := os.MkdirAll(filepath.Dir(keyPath()), 0o700); err != nil {
		return fmt.Errorf("preparing the key directory: %w", err)
	}
	if err := app.WriteFileAtomic(keyPath(), key, 0o600); err != nil {
		return fmt.Errorf("writing the fleet key: %w", err)
	}
	return nil
}

// HasSealedPasswords reports whether anything on this machine actually depends
// on the key, so the CLI can say whether keeping it matters here.
func HasSealedPasswords() bool {
	for _, n := range List() {
		if strings.HasPrefix(n.Sealed, sealPrefix) {
			return true
		}
	}
	return false
}
