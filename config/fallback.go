package config

import (
	"fmt"
	"time"
)

// DefaultFallbackDwell is how long one transport candidate is held before the
// chain moves on. A minute is long enough that a slow path is not mistaken for
// a blocked one, and short enough that an operator watching a tunnel come back
// does not give up first.
const DefaultFallbackDwell = 60 * time.Second

// Dwell turns the configured seconds into a duration, falling back to the
// default. Defined once so the two ends cannot disagree by construction.
func Dwell(seconds int) time.Duration {
	if seconds <= 0 {
		return DefaultFallbackDwell
	}
	return time.Duration(seconds) * time.Second
}

// fallbackCapable lists the transports a reverse tunnel can fall back to.
//
// It is the reverse-tunnel set minus spoof, which cmd/defaults.go already
// refuses here because it is an l3 carrier. Membership is checked rather than
// assumed so a typo in the list is reported at load time instead of producing a
// chain that silently skips a candidate it does not recognise.
var fallbackCapable = map[TransportType]bool{
	TCP: true, TCPMUX: true, STEALTH: true,
	WS: true, WSS: true, WSMUX: true, WSSMUX: true,
	KCP: true, QUIC: true, UDP: true, XDI: true, PCK: true,
}

// ValidateFallbackTransports checks a configured chain.
//
// The rules are deliberately narrow. A candidate must be a reverse transport,
// and the whole list must not contradict the tunnel's own transport by being
// empty of it — the primary is prepended by the chain, so naming it again is
// allowed but pointless, and naming nothing is the ordinary single-transport
// case.
func ValidateFallbackTransports(primary TransportType, list []TransportType) error {
	if len(list) == 0 {
		return nil
	}
	if !fallbackCapable[primary] {
		return fmt.Errorf("transport %q cannot take part in a fallback chain", primary)
	}
	seen := map[TransportType]bool{}
	for _, t := range list {
		if !fallbackCapable[t] {
			return fmt.Errorf("fallback_transports: %q is not a reverse tunnel transport", t)
		}
		if seen[t] {
			return fmt.Errorf("fallback_transports: %q is listed twice", t)
		}
		seen[t] = true
	}
	return nil
}

// FallbackNames renders the chain the way the chain package wants it.
func FallbackNames(list []TransportType) []string {
	out := make([]string, 0, len(list))
	for _, t := range list {
		out = append(out, string(t))
	}
	return out
}
