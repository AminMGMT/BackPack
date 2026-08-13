package manage

import "testing"

// Preset invariants.
//
// These numbers decide how fast a tunnel can possibly go, and they fail
// quietly: a window that is too small does not error, it just caps throughput
// at a fraction of the link and looks like "the tunnel is slow". So the limits
// that matter are asserted rather than left to review.

// presetOf builds a spec the way setup does.
func presetOf(t *testing.T, name string) TunnelSpec {
	t.Helper()
	var s TunnelSpec
	ApplyPreset(&s, name)
	return s
}

// smux refuses a session whose per-stream window exceeds the session window,
// so a preset that got this backwards would break every mux transport at
// connect time.
func TestPresetStreamBufferFitsInReceiveBuffer(t *testing.T) {
	for _, p := range []string{PresetBalance, PresetTurbo, PresetAggressive} {
		s := presetOf(t, p)
		if s.MuxStreamBuffer > s.MuxRecvBuffer {
			t.Fatalf("%s: MuxStreamBuffer (%d) > MuxRecvBuffer (%d) — smux rejects this",
				p, s.MuxStreamBuffer, s.MuxRecvBuffer)
		}
	}
}

// Single-connection throughput over a mux transport is MuxStreamBuffer / RTT.
// At the ~100 ms of an Iran-to-Europe path, 64 KB is about 5 Mbit/s, which is
// what made the mux transports feel slow regardless of the link. Every preset
// must leave room for materially more than that.
func TestPresetStreamWindowIsNotTheBottleneck(t *testing.T) {
	const rttSeconds = 0.1
	minMbit := map[string]float64{
		PresetBalance:    15,  // light, but not throttled
		PresetTurbo:      100, // the recommended default should feel fast
		PresetAggressive: 400, // "maximum throughput" has to mean it
	}
	for p, want := range minMbit {
		s := presetOf(t, p)
		gotMbit := float64(s.MuxStreamBuffer) * 8 / rttSeconds / 1e6
		if gotMbit < want {
			t.Fatalf("%s: one connection caps at %.0f Mbit/s (stream window %d bytes), want at least %.0f",
				p, gotMbit, s.MuxStreamBuffer, want)
		}
	}
}

// KCP's ceiling is window × MTU / RTT. On the "UDP + KCP + FEC" gaming
// transport the window is deliberately kept near the bandwidth-delay product —
// past it, with congestion control off, extra window is only bufferbloat — so
// these floors are lower than a bulk mover's, but still well clear of throttling
// the game traffic plus a download sharing the tunnel.
func TestPresetKCPWindowIsNotTheBottleneck(t *testing.T) {
	const rttSeconds = 0.1
	minMbit := map[string]float64{
		PresetBalance:    50,
		PresetTurbo:      100,
		PresetAggressive: 200,
	}
	for p, want := range minMbit {
		s := presetOf(t, p)
		if s.KCPMTU <= 0 || s.KCPSndWnd <= 0 {
			t.Fatalf("%s: KCP window/MTU unset (%d/%d)", p, s.KCPSndWnd, s.KCPMTU)
		}
		gotMbit := float64(s.KCPSndWnd) * float64(s.KCPMTU) * 8 / rttSeconds / 1e6
		if gotMbit < want {
			t.Fatalf("%s: KCP caps at %.0f Mbit/s (window %d × MTU %d), want at least %.0f",
				p, gotMbit, s.KCPSndWnd, s.KCPMTU, want)
		}
	}
}

// Each preset must be strictly faster than the one below it, or the names are
// lying to whoever picks one.
func TestPresetsAreOrderedBySpeed(t *testing.T) {
	bal, tur, agg := presetOf(t, PresetBalance), presetOf(t, PresetTurbo), presetOf(t, PresetAggressive)

	for _, c := range []struct {
		what           string
		low, mid, high int
	}{
		{"MuxStreamBuffer", bal.MuxStreamBuffer, tur.MuxStreamBuffer, agg.MuxStreamBuffer},
		{"MuxRecvBuffer", bal.MuxRecvBuffer, tur.MuxRecvBuffer, agg.MuxRecvBuffer},
		{"KCPSndWnd", bal.KCPSndWnd, tur.KCPSndWnd, agg.KCPSndWnd},
		{"ConnectionPool", bal.ConnectionPool, tur.ConnectionPool, agg.ConnectionPool},
	} {
		if !(c.low < c.mid && c.mid < c.high) {
			t.Fatalf("%s is not ordered Balance < Turbo < Aggressive: %d, %d, %d",
				c.what, c.low, c.mid, c.high)
		}
	}
}

// "UDP + KCP + FEC" is a gaming transport, so FEC is not optional: every preset
// must carry parity, or the name is lying and a lost game packet stalls waiting
// for a retransmit. A stronger preset tolerates more loss, so its parity ratio
// climbs (or holds), never drops — the opposite of a bulk mover, which would
// spend as little on parity as it could get away with.
func TestEveryPresetCarriesFECAndParityRisesWithStrength(t *testing.T) {
	bal, tur, agg := presetOf(t, PresetBalance), presetOf(t, PresetTurbo), presetOf(t, PresetAggressive)
	for _, s := range []struct {
		name string
		spec TunnelSpec
	}{{"Balance", bal}, {"Turbo", tur}, {"Aggressive", agg}} {
		if s.spec.KCPDataShards <= 0 || s.spec.KCPParityShards <= 0 {
			t.Fatalf("%s carries no FEC (%d:%d) — the gaming transport must always repair loss",
				s.name, s.spec.KCPDataShards, s.spec.KCPParityShards)
		}
	}
	if !(bal.KCPParityShards <= tur.KCPParityShards && tur.KCPParityShards <= agg.KCPParityShards) {
		t.Fatalf("parity must not fall as the preset strengthens: %d, %d, %d",
			bal.KCPParityShards, tur.KCPParityShards, agg.KCPParityShards)
	}
}
