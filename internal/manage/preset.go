package manage

// Performance presets.
//
// A preset is a single choice that fills every tuning knob of a tunnel —
// buffers, pool sizes, mux windows and (for KCP) the retransmission and FEC
// settings. The same three presets apply to every transport, so the answer to
// "how hard should this tunnel push?" is the same question everywhere.
//
// Backwards compatibility note: PresetTurbo reproduces the exact values that
// the old "Best Performance" preset wrote, so a config created by an earlier
// version is byte-for-byte a Turbo config. Upgrading never changes a running
// tunnel's behaviour, and a config with no preset field at all keeps whatever
// values it already has on disk.
const (
	PresetBalance    = "balance"
	PresetTurbo      = "turbo"
	PresetAggressive = "aggressive"
)

// presetOptions is the ordered list shown in the setup and edit menus.
var presetOptions = []struct {
	label, desc, value string
}{
	{"Balance", "light on CPU and RAM — best for small or shared VPS", PresetBalance},
	{"Turbo", "recommended — the tuned default for Iran to abroad links", PresetTurbo},
	{"Aggressive", "maximum throughput, noticeably more CPU — for strong servers", PresetAggressive},
}

// validPreset reports whether p is one of the three presets.
func validPreset(p string) bool {
	switch p {
	case PresetBalance, PresetTurbo, PresetAggressive:
		return true
	}
	return false
}

// presetLabel returns the display name of a preset value.
func presetLabel(value string) string {
	for _, o := range presetOptions {
		if o.value == value {
			return o.label
		}
	}
	if value == "" {
		return "Custom"
	}
	return value
}

// ApplyPreset fills every tuning field of a spec from the named preset. It is
// the single place where the numbers behind Balance/Turbo/Aggressive live, so
// the CLI, the edit screen and the benchmark all agree on what a preset means.
func ApplyPreset(s *TunnelSpec, preset string) {
	if !validPreset(preset) {
		preset = PresetTurbo
	}
	s.Preset = preset
	s.LogLevel = "info"
	s.Nodelay = true // disable Nagle — lowest latency on every transport

	switch preset {
	case PresetBalance:
		s.KeepAlive = 75
		s.Heartbeat = 40
		s.ChannelSize = 2048
		s.ConnectionPool = 4
		// A steady pool keeps idle CPU low, which is the whole point of Balance.
		s.AggressivePool = false
		s.SoRcvBuf = 4 * 1024 * 1024
		s.SoSndBuf = 4 * 1024 * 1024
		s.MuxCon = 4
		s.MuxVersion = 2
		s.MuxFrameSize = 32768
		s.MuxRecvBuffer = 2097152
		s.MuxStreamBuffer = 65536

	case PresetTurbo:
		// These are the historical "Best Performance" values, kept identical so
		// existing tunnels are unaffected by the rename. Do not tune them.
		s.KeepAlive = 75
		s.Heartbeat = 40
		s.ChannelSize = 4096
		s.ConnectionPool = 8 // enough warm connections without constant churn
		// AggressivePool stays OFF here: it keeps the pool topped up in a tight
		// loop and noticeably raises idle CPU. A normal pool is plenty.
		s.AggressivePool = false
		// Large per-socket buffers keep the pipe full on high-latency
		// Iran to abroad links (bandwidth-delay product), boosting throughput.
		// The kernel ceilings are raised to match by the Optimize step.
		s.SoRcvBuf = 8 * 1024 * 1024
		s.SoSndBuf = 8 * 1024 * 1024
		s.MuxCon = 8
		s.MuxVersion = 2
		s.MuxFrameSize = 32768
		s.MuxRecvBuffer = 4194304
		s.MuxStreamBuffer = 65536

	case PresetAggressive:
		s.KeepAlive = 60
		s.Heartbeat = 25
		s.ChannelSize = 8192
		s.ConnectionPool = 16
		// Refills the pool in a tight loop: lowest possible connect latency at
		// the cost of real idle CPU. Only worth it on a server with cores spare.
		s.AggressivePool = true
		s.SoRcvBuf = 16 * 1024 * 1024
		s.SoSndBuf = 16 * 1024 * 1024
		s.MuxCon = 16
		s.MuxVersion = 2
		s.MuxFrameSize = 65535
		s.MuxRecvBuffer = 8388608
		s.MuxStreamBuffer = 131072
	}

	applyKCPPreset(s, preset)
}

// applyKCPPreset fills the KCP-only knobs. They are written to the config only
// for the KCP transport, but filling them unconditionally keeps a later
// transport change (tcp -> kcp) from landing on zero values.
func applyKCPPreset(s *TunnelSpec, preset string) {
	// MTU stays below the common 1500 path MTU with room for the KCP, FEC and
	// encryption headers, so a KCP packet never fragments in transit.
	s.KCPMTU = 1350

	switch preset {
	case PresetBalance:
		// Standard-interval ARQ with congestion control left on: gentlest on
		// CPU and the friendliest to a shared link.
		s.KCPInterval = 40
		s.KCPResend = 2
		s.KCPNoDelay = 0
		s.KCPNoCongestion = 1
		s.KCPSndWnd = 256
		s.KCPRcvWnd = 512
		s.KCPAckNoDelay = false
		// No FEC: parity packets cost bandwidth on a clean link.
		s.KCPDataShards = 0
		s.KCPParityShards = 0

	case PresetTurbo:
		s.KCPInterval = 20
		s.KCPResend = 2
		s.KCPNoDelay = 1
		s.KCPNoCongestion = 1
		s.KCPSndWnd = 1024
		s.KCPRcvWnd = 1024
		s.KCPAckNoDelay = true
		// 10 data + 3 parity recovers up to 3 lost packets in every 13 without
		// waiting for a retransmit — the single biggest win on a lossy path.
		s.KCPDataShards = 10
		s.KCPParityShards = 3

	case PresetAggressive:
		s.KCPInterval = 10
		s.KCPResend = 2
		s.KCPNoDelay = 1
		s.KCPNoCongestion = 1
		s.KCPSndWnd = 2048
		s.KCPRcvWnd = 2048
		s.KCPAckNoDelay = true
		// Heavier parity: survives worse loss, at the cost of ~40% more packets.
		s.KCPDataShards = 10
		s.KCPParityShards = 4
	}
}

// PresetLabel returns the display name of a tunnel's performance preset.
func PresetLabel(name string) string {
	s, err := LoadSpec(name)
	if err != nil {
		return ""
	}
	return presetLabel(s.Preset)
}

// PresetValueLabel maps a raw preset config value to its display name
// ("turbo" → "Turbo", "" → "Custom"), for callers that already hold the
// decoded config and should not re-read it from disk.
func PresetValueLabel(value string) string { return presetLabel(value) }
