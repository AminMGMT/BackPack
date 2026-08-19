package manage

import "github.com/backpack/backpack/internal/tui"

// Presets for a direct tunnel.
//
// The reverse presets set a dozen fields — pool size, heartbeat, channel size,
// socket buffers, KCP windows — most of which a direct tunnel simply does not
// have. It keeps no connection pool, sends no heartbeat and runs no KCP; one
// mux session carries everything.
//
// So these are their own thing, and deliberately small. What actually decides
// how a direct tunnel feels is the mux flow control: how much data one stream
// may have in flight before it has to wait for the far end. That is the number
// that caps a single download, and the default is modest enough to be the
// limit long before the link is.
//
// The names match the reverse presets so the same word means the same ambition
// on either kind, even though the fields behind them differ.

// directPreset is one named set of tuning.
type directPreset struct {
	Name string

	MuxFrameSize     int
	MuxReceiveBuffer int
	MuxStreamBuffer  int
	Sessions         int
	Keepalive        int // seconds; 0 leaves it off
}

// directPresets are the choices, in the order they are offered.
//
// The stream buffer is the headline number in each. At 100 ms round trip, a
// single connection can move roughly StreamBuffer / RTT — so 256 KB is about
// 20 Mbit/s, 2 MB about 160, and 16 MB more than the link. Anyone whose single
// download feels slow while the link is fast is being limited by this and
// wants the next one up.
var directPresets = []directPreset{
	{
		Name:             PresetBalance,
		MuxFrameSize:     32768,
		MuxReceiveBuffer: 4 * 1024 * 1024,
		MuxStreamBuffer:  256 * 1024,
		Sessions:         1,
		Keepalive:        75,
	},
	{
		Name:             PresetTurbo,
		MuxFrameSize:     32768,
		MuxReceiveBuffer: 16 * 1024 * 1024,
		MuxStreamBuffer:  2 * 1024 * 1024,
		Sessions:         1,
		Keepalive:        75,
	},
	{
		Name:             PresetThroughput,
		MuxFrameSize:     65535,
		MuxReceiveBuffer: 32 * 1024 * 1024,
		MuxStreamBuffer:  16 * 1024 * 1024,
		// Several sessions, so one shaped connection is not the whole tunnel
		// and a single stalled stream cannot hold up the others behind it.
		Sessions:  4,
		Keepalive: 75,
	},
}

// findDirectPreset returns a preset by name, falling back to Turbo — the same
// default the reverse wizard uses, and the one that suits most links.
func findDirectPreset(name string) directPreset {
	for _, p := range directPresets {
		if p.Name == name {
			return p
		}
	}
	return directPresets[1]
}

// chooseDirectPreset asks which one, described by what it costs and buys
// rather than by the numbers behind it.
func chooseDirectPreset() directPreset {
	idx := tui.ChooseOpt("How should the tunnel be tuned?", []tui.Option{
		{Title: "Turbo", Desc: "the default — fast single downloads, modest memory. Start here"},
		{Title: "Balance", Desc: "smallest memory footprint, for a small VPS or many tunnels on one box"},
		{Title: "Throughput", Desc: "for a fat, high-latency link — several sessions and large buffers, wants RAM"},
	})
	switch idx {
	case 1:
		return findDirectPreset(PresetBalance)
	case 2:
		return findDirectPreset(PresetThroughput)
	default:
		return findDirectPreset(PresetTurbo)
	}
}

// apply copies a preset onto what the wizard collected. Sessions is only
// raised, never lowered: an operator who asked for more sessions by hand
// should not have that taken away by a preset chosen afterwards.
func (p directPreset) apply(s *directSpec) {
	s.Preset = p.Name
	s.MuxFrameSize = p.MuxFrameSize
	s.MuxReceiveBuffer = p.MuxReceiveBuffer
	s.MuxStreamBuffer = p.MuxStreamBuffer
	s.Keepalive = p.Keepalive
	// Nagle off, as every reverse preset does — see preset.go, which turns it
	// off "on every transport" for the same reason. It matters more here, not
	// less: one mux session carries every connection, so Nagle holding a small
	// write back until the previous one is acknowledged delays every stream on
	// the tunnel at once, not just the connection that caused it.
	//
	// This is not merely a default. The engine calls SetNoDelay(cfg.Nodelay),
	// so leaving it unset does not leave the socket alone — it actively turns
	// Nagle on, over Go's own default of off.
	s.Nodelay = true
	if p.Sessions > s.Sessions {
		s.Sessions = p.Sessions
	}
}
