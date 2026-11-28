package config

import "slices"

// EncryptionConfig is what wraps every connection of a stream tunnel (tcp,
// tcpmux, ws, wsmux, wss, wssmux): the control channel and every pooled
// connection alike. Embedded in both ends' tables, so the keys sit at the top
// level, and both ends must agree on them.
//
// Off by default. Any mode but "none" changes the wire, so a tunnel that sets
// one no longer speaks to an end without the same setting.
type EncryptionConfig struct {
	// Encryption is "none" (or empty), "stealth", "stealth+", "shadow" or
	// "scrap". See EncryptionModes for which transport carries which.
	Encryption string `toml:"encryption"`
	// Splash pads the first records of every connection to random sizes, so
	// the handshake of whatever runs inside the tunnel cannot be read off the
	// record lengths. Needs encrypted traffic: wss, wssmux, or an encryption.
	Splash bool `toml:"splash"`
	// ShadowSNI is the real site whose TLS handshake "shadow" borrows: the
	// kharej names it in its ClientHello, the Iran server relays the handshake
	// to it, so a probe sees that site's own certificate.
	ShadowSNI string `toml:"shadow_sni"`
}

// The encryption modes.
const (
	EncNone        = "none"
	EncStealth     = "stealth"
	EncStealthPlus = "stealth+"
	EncShadow      = "shadow"
	EncScrap       = "scrap"
)

// EncryptionMode is the mode in effect; empty reads as none.
func (e EncryptionConfig) EncryptionMode() string {
	if e.Encryption == "" {
		return EncNone
	}
	return e.Encryption
}

// Encrypted reports whether a mode other than none is set.
func (e EncryptionConfig) Encrypted() bool { return e.EncryptionMode() != EncNone }

// ValidEncryption reports whether v names a mode at all, whatever the
// transport.
func ValidEncryption(v string) bool {
	switch v {
	case "", EncNone, EncStealth, EncStealthPlus, EncShadow, EncScrap:
		return true
	}
	return false
}

// EncryptionModes is what a transport can carry, none first. The websocket
// family is already shaped like HTTP, so the modes that dress the stream as
// TLS or HTTP are offered on tcp and tcpmux only. A transport outside the
// stream six has no choice to make: nil.
func EncryptionModes(tr TransportType) []string {
	switch tr {
	case TCP, TCPMUX:
		return []string{EncNone, EncStealth, EncStealthPlus, EncScrap}
	case WS, WSS, WSMUX, WSSMUX:
		return []string{EncNone, EncStealth, EncStealthPlus}
	}
	return nil
}

// ManagedEncryptionModes is what a tunnel under the Vision helper can carry
// inside it, none first. The helper already looks like TLS to the cover site,
// so only the modes that encrypt end to end are offered: what the helpers
// relay is then ciphertext, and stealth+ also hides the record sizes and
// timing a TLS-in-TLS check reads.
var ManagedEncryptionModes = []string{EncNone, EncStealth, EncStealthPlus}

// ManagedEncryption is the encryption the server runs inside its managed
// helper: stealth or stealth+, or "" for none or no helper.
func (c ServerConfig) ManagedEncryption() string {
	return managedEncryption(c.Xray.Vision(), c.EncryptionConfig)
}

// ManagedEncryption is the client's; see ServerConfig.ManagedEncryption.
func (c ClientConfig) ManagedEncryption() string {
	return managedEncryption(c.Xray.Vision(), c.EncryptionConfig)
}

func managedEncryption(vision bool, e EncryptionConfig) string {
	if mode := e.EncryptionMode(); vision && mode != EncNone && slices.Contains(ManagedEncryptionModes, mode) {
		return mode
	}
	return ""
}

// EncryptionAllowed reports whether tr can carry mode.
func EncryptionAllowed(tr TransportType, mode string) bool {
	if mode == "" || mode == EncNone {
		return true
	}
	return slices.Contains(EncryptionModes(tr), mode)
}

// SplashAllowed reports whether splash can be set on tr with mode: padding in
// the clear hides nothing, so it needs wss, wssmux or an encryption.
func SplashAllowed(tr TransportType, mode string) bool {
	switch tr {
	case WSS, WSSMUX:
		return true
	case TCP, TCPMUX, WS, WSMUX:
		return mode != "" && mode != EncNone && EncryptionAllowed(tr, mode)
	}
	return false
}
