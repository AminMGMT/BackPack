package network

import "github.com/backpack/backpack/config"

// SpoofDPIFromConfig maps the flat spoof_* obfuscation knobs of a SpoofConfig
// onto the carrier's SpoofDPI struct. It lives in the network package (which
// already depends on config) so the server and client both build the struct the
// same way from one place, rather than each repeating the field mapping.
func SpoofDPIFromConfig(sc config.SpoofConfig) SpoofDPI {
	return SpoofDPI{
		TTLJitter:   sc.SpoofTTLJitter,
		RandomDSCP:  sc.SpoofRandomDSCP,
		ShufflePort: sc.SpoofShufflePort,
		PortMin:     uint16(sc.SpoofPortMin),
		PortMax:     uint16(sc.SpoofPortMax),
		Padding:     sc.SpoofPadding,
		PaddingMax:  uint8(sc.SpoofPaddingMax),
		FakeTLS:     sc.SpoofFakeTLS,
	}
}
