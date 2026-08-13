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
//
// KCP here is the "UDP + KCP + FEC" transport: a low-latency gaming tunnel, not
// a bulk mover. So every preset shares the same latency-first ARQ — NoDelay on,
// a 10 ms tick, fast-retransmit at 2 duplicate ACKs, KCP's own congestion
// window off, and ACKs sent immediately — and every preset carries FEC, because
// on a game path repairing a lost packet from parity beats waiting a whole RTT
// for a retransmit. What the preset changes is only how much headroom it buys:
// the window (which, with congestion control off, is also the bound on how much
// data can queue and inflate ping) and the parity ratio (how much loss it can
// absorb before a stall shows through).
func applyKCPPreset(s *TunnelSpec, preset string) {
	// MTU stays below the common 1500 path MTU with room for the KCP, FEC and
	// encryption headers, so a KCP packet never fragments in transit.
	s.KCPMTU = 1350

	// Latency-first ARQ, identical on every preset. This is the "fast mode"
	// KCP was built for: NoDelay skips the delayed-ACK wait, a 10 ms tick (the
	// floor kcp-go allows) flushes retransmits promptly, Resend=2 retransmits a
	// segment after two duplicate ACKs instead of on a timer, NoCongestion
	// takes KCP's own AIMD window out of the path so one loss does not halve the
	// rate, and AckNoDelay returns each ACK at once so the sender learns of a
	// loss a round trip sooner. Together they trade bandwidth-efficiency for the
	// steady, low ping a game needs.
	s.KCPInterval = 10
	s.KCPResend = 2
	s.KCPNoDelay = 1
	s.KCPNoCongestion = 1
	s.KCPAckNoDelay = true

	switch preset {
	case PresetBalance:
		// The lightest gaming profile. With congestion control off the window
		// is the ceiling on in-flight data, so keeping it small is what keeps
		// buffering — and therefore worst-case ping — bounded on a modest link.
		// 512 × 1350 / 100 ms ≈ 55 Mbit/s, ample for game traffic plus light
		// browsing through the same tunnel.
		s.KCPSndWnd = 512
		s.KCPRcvWnd = 512
		// 10:2 repairs up to 2 lost packets in every 12 (~17% loss) for a 20%
		// parity cost — the floor that still makes "+ FEC" mean something on a
		// clean-ish route.
		s.KCPDataShards = 10
		s.KCPParityShards = 2

	case PresetTurbo:
		// The recommended default. Twice the window — ~110 Mbit/s of headroom at
		// 100 ms — for room to also pull a download through the tunnel without
		// starving the game packets, still small enough to keep queueing low.
		s.KCPSndWnd = 1024
		s.KCPRcvWnd = 1024
		// 10:3 (~33% loss tolerated, 30% overhead): the middle of the loss the
		// tuned Iran-to-abroad routes this preset targets actually see.
		s.KCPDataShards = 10
		s.KCPParityShards = 3

	case PresetAggressive:
		// The strongest profile, for a bad route on a server with headroom.
		// 2048 × 1350 / 100 ms ≈ 220 Mbit/s — larger, but deliberately far below
		// the old 8192: past the bandwidth-delay product the extra window buys a
		// gaming tunnel nothing but bufferbloat.
		s.KCPSndWnd = 2048
		s.KCPRcvWnd = 2048
		// 10:4 (~40% loss tolerated, 40% overhead): the most parity worth
		// spending before a different route is the better answer.
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
