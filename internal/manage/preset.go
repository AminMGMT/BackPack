package manage

// Performance presets.
//
// A preset is a single choice that fills every tuning knob of a tunnel —
// buffers, pool sizes, mux windows and (for KCP) the retransmission and FEC
// settings. The same three presets apply to every transport, so the answer to
// "how hard should this tunnel push?" is the same question everywhere.
//
// Upgrade note: a preset is applied once, when a tunnel is created or when the
// operator picks "Change performance preset". The numbers are written into the
// tunnel's config file, and an update replaces only the binary — it never
// rewrites a config. So changing the values here cannot disturb a tunnel that
// already exists: it keeps the numbers on its disk until somebody deliberately
// re-applies a preset. New tunnels get the current values, and a config with no
// preset field at all is left exactly as it is.
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
		// Sizes the datagram socket only; TCP is auto-tuned by the kernel.
		s.SoRcvBuf = 4 * 1024 * 1024
		s.SoSndBuf = 4 * 1024 * 1024
		s.MuxCon = 4
		s.MuxVersion = 2
		s.MuxFrameSize = 32768
		// 256 KB per stream ≈ 20 Mbit/s for one connection at 100 ms — modest
		// on purpose, but four times what 64 KB allowed. Worst-case memory is
		// MuxCon × MuxRecvBuffer = 4 × 4 MB.
		s.MuxRecvBuffer = 4 * 1024 * 1024
		s.MuxStreamBuffer = 256 * 1024

	case PresetTurbo:
		s.KeepAlive = 75
		s.Heartbeat = 40
		s.ChannelSize = 4096
		s.ConnectionPool = 8 // enough warm connections without constant churn
		// AggressivePool stays OFF here: it keeps the pool topped up in a tight
		// loop and noticeably raises idle CPU. A normal pool is plenty.
		s.AggressivePool = false
		// Sizes the datagram socket only; TCP is auto-tuned by the kernel.
		s.SoRcvBuf = 8 * 1024 * 1024
		s.SoSndBuf = 8 * 1024 * 1024
		s.MuxCon = 8
		s.MuxVersion = 2
		s.MuxFrameSize = 32768
		// 2 MB per stream ≈ 160 Mbit/s for a single connection at 100 ms RTT.
		// This is the number that decides how fast one download feels, and the
		// old 64 KB capped it at about 5 Mbit/s on that same path — the mux
		// transports were being throttled by their own flow control long before
		// the link ran out. Worst-case memory is MuxCon × MuxRecvBuffer = 8 × 16 MB.
		s.MuxRecvBuffer = 16 * 1024 * 1024
		s.MuxStreamBuffer = 2 * 1024 * 1024

	case PresetAggressive:
		s.KeepAlive = 60
		s.Heartbeat = 25
		s.ChannelSize = 8192
		s.ConnectionPool = 16
		// Refills the pool in a tight loop: lowest possible connect latency at
		// the cost of real idle CPU. Only worth it on a server with cores spare.
		s.AggressivePool = true
		// Sizes the datagram socket only; TCP is auto-tuned by the kernel.
		s.SoRcvBuf = 16 * 1024 * 1024
		s.SoSndBuf = 16 * 1024 * 1024
		s.MuxCon = 16
		s.MuxVersion = 2
		s.MuxFrameSize = 65535
		// 8 MB per stream ≈ 640 Mbit/s for a single connection at 100 ms RTT,
		// which is what "maximum throughput" has to mean on this route. The
		// memory is a ceiling on data actually in flight, not an allocation,
		// but the worst case is real: MuxCon × MuxRecvBuffer = 16 × 32 MB, so
		// this preset wants a server with RAM to spare — as it says it does.
		s.MuxRecvBuffer = 32 * 1024 * 1024
		s.MuxStreamBuffer = 8 * 1024 * 1024
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
		s.KCPInterval = 30
		s.KCPResend = 2
		s.KCPNoDelay = 1
		s.KCPNoCongestion = 1
		// The window is the throughput ceiling: window × MTU / RTT. At 1350 MTU
		// and 100 ms, 1024 packets is about 110 Mbit/s — plenty for the preset
		// that exists to be light, and four times what 256 allowed.
		s.KCPSndWnd = 1024
		s.KCPRcvWnd = 1024
		s.KCPAckNoDelay = false
		// No FEC: parity packets cost bandwidth on a clean link.
		s.KCPDataShards = 0
		s.KCPParityShards = 0

	case PresetTurbo:
		// A 10 ms tick flushes four times as often as the old 40 ms one, which
		// is what turns a full window into steady throughput instead of bursts.
		s.KCPInterval = 10
		s.KCPResend = 2
		s.KCPNoDelay = 1
		s.KCPNoCongestion = 1
		// 2048 × 1350 / 100 ms ≈ 220 Mbit/s. The old 1024 halved that.
		s.KCPSndWnd = 2048
		s.KCPRcvWnd = 2048
		s.KCPAckNoDelay = true
		// 10 data + 2 parity repairs up to 2 lost packets in every 12 without
		// waiting for a retransmit. It was 10:3, which spends 30% of the link
		// on parity; 20% keeps most of the benefit and gives the rest back as
		// throughput, which is what this preset is for.
		s.KCPDataShards = 10
		s.KCPParityShards = 2

	case PresetAggressive:
		s.KCPInterval = 10
		s.KCPResend = 2
		s.KCPNoDelay = 1
		s.KCPNoCongestion = 1
		// 8192 × 1350 / 100 ms ≈ 880 Mbit/s: enough window that the link, not
		// the protocol, is what runs out first. Costs about 11 MB of send and
		// receive buffer per session, which is the trade this preset exists to
		// make.
		s.KCPSndWnd = 8192
		s.KCPRcvWnd = 8192
		s.KCPAckNoDelay = true
		// 10:3 rather than 10:4 — 40% parity is a lot of link to spend on a
		// preset whose whole promise is throughput. Where the route is bad
		// enough to need more, Link Test says so.
		s.KCPDataShards = 10
		s.KCPParityShards = 3
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
