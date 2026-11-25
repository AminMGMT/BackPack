package config

import (
	"slices"
	"testing"
)

func TestEncryptionModesPerTransport(t *testing.T) {
	for _, tr := range []TransportType{TCP, TCPMUX, WS, WSS, WSMUX, WSSMUX} {
		modes := EncryptionModes(tr)
		if len(modes) < 2 || modes[0] != EncNone || !slices.Contains(modes, EncStealth) {
			t.Errorf("%s carries %v, want none first and stealth", tr, modes)
		}
	}
	for _, tr := range []TransportType{UDP, KCP, QUIC, STEALTH, PCK} {
		if modes := EncryptionModes(tr); modes != nil {
			t.Errorf("%s is not a stream transport of the plain engine, yet carries %v", tr, modes)
		}
		if EncryptionAllowed(tr, EncStealth) {
			t.Errorf("%s allowed stealth", tr)
		}
		if !EncryptionAllowed(tr, "") || !EncryptionAllowed(tr, EncNone) {
			t.Errorf("%s refused none", tr)
		}
	}
}

func TestEncryptionModeReadsEmptyAsNone(t *testing.T) {
	var e EncryptionConfig
	if e.EncryptionMode() != EncNone || e.Encrypted() {
		t.Fatalf("an empty encryption reads as %q", e.EncryptionMode())
	}
	e.Encryption = EncStealth
	if !e.Encrypted() {
		t.Fatal("stealth reads as not encrypted")
	}
	for _, v := range []string{"", "none", "stealth", "stealth+", "shadow", "scrap"} {
		if !ValidEncryption(v) {
			t.Errorf("%q refused", v)
		}
	}
	if ValidEncryption("aes") {
		t.Error("an unknown mode was accepted")
	}
}
