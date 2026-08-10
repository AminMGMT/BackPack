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

// KCP's ceiling is window × MTU / RTT, so the same reasoning applies to the
// datagram transport's own window.
func TestPresetKCPWindowIsNotTheBottleneck(t *testing.T) {
	const rttSeconds = 0.1
	minMbit := map[string]float64{
		PresetBalance:    80,
		PresetTurbo:      180,
		PresetAggressive: 600,
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

// FEC costs link capacity, so a preset promising throughput must not spend
// more of it than the one below. Parity is expressed per 10 data shards.
func TestPresetFECOverheadDoesNotGrowWithSpeed(t *testing.T) {
	tur, agg := presetOf(t, PresetTurbo), presetOf(t, PresetAggressive)
	if presetOf(t, PresetBalance).KCPParityShards != 0 {
		t.Fatal("Balance should carry no FEC overhead on a clean link")
	}
	if tur.KCPParityShards > 3 || agg.KCPParityShards > 3 {
		t.Fatalf("parity too heavy for a throughput preset: turbo %d, aggressive %d",
			tur.KCPParityShards, agg.KCPParityShards)
	}
}

func TestPresetResourceFootprintsAreBounded(t *testing.T) {
	for _, preset := range []string{PresetBalance, PresetTurbo, PresetAggressive} {
		s := presetOf(t, preset)
		if footprint := s.ConnectionPool * s.MuxRecvBuffer; footprint > 64<<20 {
			t.Errorf("%s SMUX receive-window footprint = %d MiB, want <= 64 MiB", preset, footprint>>20)
		}
		if s.ChannelSize > 4096 {
			t.Errorf("%s channel_size = %d, want <= 4096", preset, s.ChannelSize)
		}
		if s.SoRcvBuf > 4<<20 || s.SoSndBuf > 4<<20 {
			t.Errorf("%s socket buffers = %d/%d, want <= 4 MiB", preset, s.SoRcvBuf, s.SoSndBuf)
		}
	}
}
