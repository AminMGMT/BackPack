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
	// PresetThroughput is the odd one out, and deliberately so.
	//
	// Balance, Turbo and Aggressive are all gaming profiles: they differ only in
	// how much headroom they buy, and every one of them spends bandwidth to hold
	// the ping steady — immediate ACKs, a 10 ms tick, and enough parity to repair
	// a lost packet rather than wait a round trip for it to be resent. That is
	// the right trade for a game and the wrong one for a download, and no amount
	// of tuning inside those three fixes it, because the cost is the point.
	//
	// This preset makes the opposite trade on the same transport: batch the ACKs,
	// tick half as often, carry a tenth as much parity, and open the window wide
	// enough that one stream can actually fill a long fat path. It is for the
	// operator who wants the tunnel to move data quickly and is not measuring
	// their ping while it does.
	//
	// It is offered on the KCP family only. On a TCP-based transport every knob
	// it changes belongs to the kernel's stack rather than to this process, so
	// choosing it there would change nothing while implying it had.
	PresetThroughput = "throughput"
)

// presetOptions is the ordered list shown in the setup and edit menus. kcpOnly
// marks a preset that only means something on a transport this process runs the
// congestion control for.
var presetOptions = []struct {
	label, desc, value string
	kcpOnly            bool
}{
	{"Balance", "light on CPU and RAM — best for small or shared VPS", PresetBalance, false},
	{"Turbo", "recommended — the tuned default for Iran to abroad links", PresetTurbo, false},
	{"Aggressive", "maximum gaming headroom, noticeably more CPU — for strong servers", PresetAggressive, false},
	{"Throughput", "maximum bandwidth for udp+kcp+fec — trades steady ping for speed", PresetThroughput, true},
}

// validPreset reports whether p names a preset at all. It deliberately does not
// ask which transport is in play: a config that already names a preset must keep
// loading, and the transport check belongs where a choice is being made.
func validPreset(p string) bool {
	switch p {
	case PresetBalance, PresetTurbo, PresetAggressive, PresetThroughput:
		return true
	}
	return false
}

// presetSuitsTransport reports whether a preset can be chosen for a transport.
// Only the KCP family runs its own congestion control, retransmission and FEC in
// this process, so only there does the Throughput profile change anything.
func presetSuitsTransport(preset, transport string) bool {
	if preset != PresetThroughput {
		return true
	}
	return isKCP(transport)
}

// PresetSuitsTransport is the exported form, for the web panel's validation.
func PresetSuitsTransport(preset, transport string) bool {
	return presetSuitsTransport(preset, transport)
}

// presetOptionsFor returns the presets offerable for a transport, in menu order.
func presetOptionsFor(transport string) []struct {
	label, desc, value string
	kcpOnly            bool
} {
	out := presetOptions[:0:0]
	for _, o := range presetOptions {
		if o.kcpOnly && !isKCP(transport) {
			continue
		}
		out = append(out, o)
	}
	return out
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

	case PresetThroughput:
		s.KeepAlive = 75
		s.Heartbeat = 40
		s.ChannelSize = 8192
		s.ConnectionPool = 16
		// Off on purpose, unlike Aggressive. Topping the pool up in a tight loop
		// buys lower connect latency for a new stream and costs real idle CPU —
		// worth it when every millisecond of a game's first packet counts,
		// pointless for a transfer that runs for minutes once it has started.
		s.AggressivePool = false
		// Sizes the UDP socket the whole tunnel shares. A window this large can
		// have far more in flight than the default socket buffer will hold, and a
		// datagram dropped by a full socket buffer is indistinguishable on the
		// wire from one the network lost — it just triggers a retransmit and
		// halves nothing, because congestion control is off. So it is generous.
		s.SoRcvBuf = 32 * 1024 * 1024
		s.SoSndBuf = 32 * 1024 * 1024
		s.MuxCon = 8
		s.MuxVersion = 2
		s.MuxFrameSize = 65535
		// 16 MB per stream is what makes a single download fast on a long path:
		// it is the flow-control ceiling one connection can have outstanding, so
		// at 200 ms round trip it allows roughly 640 Mbit/s for one stream, where
		// Turbo's 2 MB would cap that same stream near 80. Worst case is
		// MuxCon × MuxRecvBuffer = 8 × 32 MB, the same ceiling Aggressive already
		// asks for, so this needs a server with RAM but no more than that one.
		s.MuxRecvBuffer = 32 * 1024 * 1024
		s.MuxStreamBuffer = 16 * 1024 * 1024
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

	case PresetThroughput:
		// Everything here undoes a latency-first default above, and each one is
		// worth a measurable amount of bandwidth.
		//
		// A 20 ms tick instead of 10 halves the number of times a second the
		// session walks its send queue. With a window this large that walk is not
		// free, and on a small VPS the tick is what puts a ceiling on packets per
		// second long before the link does.
		s.KCPInterval = 20
		// Batch the acknowledgements. AckNoDelay returns an ACK the instant a
		// packet lands, which is how a game learns of a loss a round trip sooner
		// — and on a saturated transfer it very nearly doubles the packets on the
		// wire, every one of them competing with the data for the same link.
		s.KCPAckNoDelay = false
		// 4096 × 1298 bytes ≈ 5.3 MB in flight. This is the number that decides
		// the ceiling on a long path: at 200 ms round trip it allows roughly
		// 210 Mbit/s for a single stream, where Aggressive's 2048 caps the same
		// stream near 105. It is also why this is not a gaming preset — with
		// congestion control off, a window that large is a queue that deep, and
		// a queue that deep is bufferbloat under load.
		s.KCPSndWnd = 4096
		s.KCPRcvWnd = 4096
		// 10:1 — about 10% overhead instead of Aggressive's 40%. Parity is a
		// straight tax on bandwidth: every parity packet is a packet of capacity
		// not carrying data. A gaming preset pays it gladly, because repairing a
		// loss from parity beats waiting a round trip for the retransmit. A
		// transfer does not care when a byte arrives, only how many arrive a
		// second, so it keeps just enough parity to absorb ordinary jitter-loss
		// and lets ARQ handle the rest.
		s.KCPDataShards = 10
		s.KCPParityShards = 1
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
