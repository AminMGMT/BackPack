package webui

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// RFC 6238's own test vectors, for the SHA-1 variant.
//
// The whole value of implementing this rather than importing it rests on being
// able to show it is the algorithm every authenticator app implements. These
// are the numbers in the RFC, taken against its published secret — if the
// implementation drifts, every phone in the fleet stops matching at once and
// nothing else in this codebase would say so.
//
// The RFC's vectors are 8 digits; this panel issues 6, which is the low six of
// the same value, because the truncation is taken modulo 10^digits.
func TestTheCodesAreTheOnesRFC6238Publishes(t *testing.T) {
	// "12345678901234567890" — the RFC's SHA-1 seed, in base32.
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).
		EncodeToString([]byte("12345678901234567890"))

	for _, tc := range []struct {
		unix int64
		want string // the low six digits of the RFC's eight
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	} {
		got, err := totpCode(secret, time.Unix(tc.unix, 0))
		if err != nil {
			t.Fatalf("at %d: %v", tc.unix, err)
		}
		if got != tc.want {
			t.Errorf("at %d the code is %s, and RFC 6238 says %s", tc.unix, got, tc.want)
		}
	}
}

func TestAFreshSecretProducesACodeItsOwnCheckerAccepts(t *testing.T) {
	secret, err := newTOTPSecret()
	if err != nil {
		t.Fatalf("generating a secret: %v", err)
	}
	now := time.Now()
	code, err := totpCode(secret, now)
	if err != nil {
		t.Fatalf("computing a code: %v", err)
	}
	if !totpValid(secret, code, now) {
		t.Fatal("the checker refused a code it had just produced")
	}
}

// A phone's clock and a server's clock are both set by NTP and still disagree
// occasionally. One step either way is accepted; two is not, or the window
// stops being a window.
func TestOneStepOfClockDriftIsForgivenAndTwoIsNot(t *testing.T) {
	secret, _ := newTOTPSecret()
	now := time.Now()

	for _, drift := range []time.Duration{-totpStep, 0, totpStep} {
		code, _ := totpCode(secret, now.Add(drift))
		if !totpValid(secret, code, now) {
			t.Errorf("a code %s out was refused", drift)
		}
	}
	for _, drift := range []time.Duration{-3 * totpStep, 3 * totpStep} {
		code, _ := totpCode(secret, now.Add(drift))
		if totpValid(secret, code, now) {
			t.Errorf("a code %s out was accepted", drift)
		}
	}
}

func TestNonsenseIsRefusedRatherThanErroring(t *testing.T) {
	secret, _ := newTOTPSecret()
	now := time.Now()
	for _, given := range []string{"", "12345", "1234567", "abcdef", "   ", "000000000000"} {
		if totpValid(secret, given, now) {
			t.Errorf("%q was accepted as a code", given)
		}
	}
	// A secret that is not base32 must refuse everything rather than panic.
	if totpValid("not base32!!", "123456", now) {
		t.Error("an unreadable secret accepted a code")
	}
}

// The URI is what an authenticator app reads. If it is malformed the operator
// has to type the secret by hand, and an operator doing that is an operator who
// turns the second factor off.
func TestTheProvisioningURIIsOneAnAppCanRead(t *testing.T) {
	uri := otpauthURI("JBSWY3DPEHPK3PXP", "iran-1")

	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("not an otpauth URI: %s", uri)
	}
	for _, want := range []string{
		"secret=JBSWY3DPEHPK3PXP", "issuer=Backpack",
		"algorithm=SHA1", "digits=6", "period=30",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("the URI does not carry %s: %s", want, uri)
		}
	}
	// The hostname is in the label so one phone can hold several panels.
	if !strings.Contains(uri, "iran-1") {
		t.Errorf("the URI does not name the server: %s", uri)
	}
}

// Recovery codes are the difference between a second factor and a way to lose a
// server. Each one works once.
func TestARecoveryCodeWorksOnceAndOnlyOnce(t *testing.T) {
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		t.Fatalf("generating recovery codes: %v", err)
	}
	if len(codes) != recoveryCodeCount || len(hashes) != recoveryCodeCount {
		t.Fatalf("got %d codes and %d hashes, want %d of each", len(codes), len(hashes), recoveryCodeCount)
	}

	// What is stored must not be what was shown.
	for i, code := range codes {
		if strings.Contains(hashes[i], normaliseRecoveryCode(code)) {
			t.Fatal("a recovery code is recoverable from what is stored")
		}
	}

	left, ok := useRecoveryCode(hashes, codes[3])
	if !ok {
		t.Fatal("a freshly issued recovery code was refused")
	}
	if len(left) != recoveryCodeCount-1 {
		t.Fatalf("%d codes left after using one, want %d", len(left), recoveryCodeCount-1)
	}
	if _, ok := useRecoveryCode(left, codes[3]); ok {
		t.Fatal("a recovery code worked twice — a code read off a screenshot is still live")
	}
	// The others still work.
	if _, ok := useRecoveryCode(left, codes[0]); !ok {
		t.Fatal("using one recovery code invalidated another")
	}
}

// Typing is forgiving about the things that are presentation: the grouping dash
// and the case.
func TestARecoveryCodeIsAcceptedHoweverItIsTyped(t *testing.T) {
	codes, hashes, _ := newRecoveryCodes()
	plain := strings.ReplaceAll(codes[0], "-", "")

	for _, given := range []string{codes[0], plain, strings.ToUpper(codes[0]), "  " + codes[0] + "  "} {
		if _, ok := useRecoveryCode(hashes, given); !ok {
			t.Errorf("%q was refused, and it is the same code", given)
		}
	}
}

func TestAWrongRecoveryCodeChangesNothing(t *testing.T) {
	_, hashes, _ := newRecoveryCodes()
	left, ok := useRecoveryCode(hashes, "0000-000000")
	if ok {
		t.Fatal("a code nobody issued was accepted")
	}
	if len(left) != len(hashes) {
		t.Fatalf("a wrong code consumed one: %d left of %d", len(left), len(hashes))
	}
}
