package config

import "strings"

// Spoof-carrier mode resolution, kept in one place so every consumer (the
// server, the client, and the startup validation) agrees on what "relay mode"
// means and where it forwards — and so the legacy spoof_pipe keys keep working
// without that alias logic being scattered across the dispatch sites.

// SpoofModeKCP and SpoofModeRelay are the two shapes the spoof carrier can take.
const (
	SpoofModeKCP   = "kcp"
	SpoofModeRelay = "relay"
)

// spoofRelayDefaultAddr is the local UDP target the relay uses when neither
// spoof_forward nor the legacy spoof_pipe_addr is set. 51820 is WireGuard's
// default port — the most common thing carried over the relay — but the target
// is arbitrary.
const spoofRelayDefaultAddr = "127.0.0.1:51820"

// RelayMode reports whether the spoof carrier should run as a bare datagram
// relay rather than a KCP tunnel. spoof_mode = "relay" selects it explicitly;
// the legacy spoof_pipe = true is honoured as the same thing.
func (s SpoofConfig) RelayMode() bool {
	if strings.EqualFold(strings.TrimSpace(s.SpoofMode), SpoofModeRelay) {
		return true
	}
	return s.SpoofPipe
}

// RelayForward is the relay mode's local UDP target: spoof_forward if set,
// otherwise the legacy spoof_pipe_addr, otherwise the default. Meaningless in
// kcp mode.
func (s SpoofConfig) RelayForward() string {
	if addr := strings.TrimSpace(s.SpoofForward); addr != "" {
		return addr
	}
	if addr := strings.TrimSpace(s.SpoofPipeAddr); addr != "" {
		return addr
	}
	return spoofRelayDefaultAddr
}
