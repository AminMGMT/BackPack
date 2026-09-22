package webui

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// A second factor for the panel.
//
// The panel is root on the machine. Everything it can do — edit a tunnel,
// restore a backup, install an update, read a token — is done as root, and the
// whole of it was behind one password. A password is the credential most likely
// to be reused, phished or read out of another breach, and the panel is on a
// public port by necessity.
//
// So: a time-based one-time code, RFC 6238, in about a hundred lines and with
// no dependency. There is nothing to a TOTP but an HMAC over a counter and a
// truncation, and the algorithm has not changed since 2011. A library would be
// a supply-chain surface for code this size.
//
// # The choices, and why
//
// **SHA-1, 6 digits, 30 seconds.** Not because they are the strongest options
// but because they are what every authenticator app defaults to, and an
// operator who has to hand-configure an entry is an operator who gives up and
// leaves the second factor off. The HMAC's key is a fresh 160-bit random
// secret, and SHA-1 in HMAC is not affected by the collision attacks that
// retired it for signatures.
//
// **One step of tolerance either way.** A phone's clock and a VPS's clock are
// both set by NTP and are rarely more than a second or two apart, but "rarely"
// is not "never", and a code refused because a clock drifted is an operator
// locked out of their own server. Ninety seconds of acceptance costs
// essentially nothing against brute force once the login rate limiter is in
// front of it.
//
// **Recovery codes, and they are the point.** A second factor whose only key is
// a phone is a way to lose a server. Ten single-use codes are generated with
// the secret, shown once, and stored hashed — so the file on the server is not
// a way in, and a code that has been used cannot be used again.

const (
	// totpDigits is the length of a code, and it is six because that is what
	// every app shows.
	totpDigits = 6
	// totpStep is the window one code is valid for.
	totpStep = 30 * time.Second
	// totpSkew is how many steps either side of now are accepted. See above.
	totpSkew = 1
	// totpSecretBytes is the length of the shared secret. RFC 4226 requires at
	// least 128 bits and recommends 160, which is also SHA-1's output size.
	totpSecretBytes = 20
	// recoveryCodeCount is how many single-use codes are issued with a secret.
	recoveryCodeCount = 10
)

// b32 is unpadded base32, which is what an otpauth:// URI carries.
var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// newTOTPSecret returns a fresh secret, base32-encoded the way an authenticator
// app expects to be given one.
func newTOTPSecret() (string, error) {
	raw := make([]byte, totpSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating a two-factor secret: %w", err)
	}
	return b32.EncodeToString(raw), nil
}

// totpCode computes the code for one secret at one moment.
func totpCode(secret string, at time.Time) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", fmt.Errorf("the two-factor secret is not valid base32: %w", err)
	}

	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(at.Unix()/int64(totpStep.Seconds())))

	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)

	// RFC 4226 dynamic truncation: the low nibble of the last byte picks where
	// to read four bytes from, the top bit of those is cleared, and the result
	// is taken modulo 10^digits.
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff

	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, value%mod), nil
}

// totpValid reports whether given is the code for secret at or near now.
//
// The comparison is constant-time. A timing difference on six digits is not
// much of an attack, but it is free to close and the alternative is explaining
// why it was left open.
func totpValid(secret, given string, now time.Time) bool {
	given = strings.TrimSpace(given)
	if len(given) != totpDigits {
		return false
	}
	for step := -totpSkew; step <= totpSkew; step++ {
		want, err := totpCode(secret, now.Add(time.Duration(step)*totpStep))
		if err != nil {
			return false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(given)) == 1 {
			return true
		}
	}
	return false
}

// otpauthURI is what an authenticator app reads, either from a QR code or from
// the link itself. The label carries the hostname so a phone holding several
// servers' panels can tell them apart.
func otpauthURI(secret, host string) string {
	if host == "" {
		host = "panel"
	}
	label := "Backpack:" + host
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", "Backpack")
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(int(totpStep.Seconds())))
	return "otpauth://totp/" + url.PathEscape(label) + "?" + q.Encode()
}

// newRecoveryCodes returns the codes to show the operator once, and the hashes
// to keep.
//
// The codes are stored hashed for the same reason a password is: the file sits
// on the machine the panel protects, and a backup of that file travels. Reading
// it must not be a way in.
func newRecoveryCodes() (codes []string, hashes []string, err error) {
	for i := 0; i < recoveryCodeCount; i++ {
		raw := make([]byte, 5) // 10 hex characters, 40 bits
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, fmt.Errorf("generating recovery codes: %w", err)
		}
		code := hex.EncodeToString(raw)
		codes = append(codes, code[:5]+"-"+code[5:])
		hashes = append(hashes, hashRecoveryCode(code))
	}
	return codes, hashes, nil
}

// normaliseRecoveryCode makes typing forgiving: case and the grouping dash are
// presentation, not content.
func normaliseRecoveryCode(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
}

func hashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(normaliseRecoveryCode(code)))
	return hex.EncodeToString(sum[:])
}

// useRecoveryCode checks given against the stored hashes and returns what is
// left. A code works once: the hash that matched is removed, so a code read off
// a screenshot or a shell history is already spent.
func useRecoveryCode(hashes []string, given string) (remaining []string, ok bool) {
	want := hashRecoveryCode(given)
	remaining = make([]string, 0, len(hashes))
	for _, h := range hashes {
		if !ok && subtle.ConstantTimeCompare([]byte(h), []byte(want)) == 1 {
			ok = true
			continue
		}
		remaining = append(remaining, h)
	}
	return remaining, ok
}
