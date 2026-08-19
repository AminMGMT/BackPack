package manage

import (
	"fmt"

	"github.com/backpack/backpack/internal/tui"
)

// Presets for a full IP tunnel.
//
// The reverse presets and the direct layer-4 ones both tune a mux and a KCP
// layer. A layer-3 tunnel has neither, so none of those fields exist here and
// copying them would produce a config full of keys the engine ignores.
//
// What actually decides how a layer-3 tunnel feels is the queue between the
// kernel and this process, and how much room the carrier's sockets have. Both
// are a trade, and it is not the trade people expect:
//
//   - A deeper queue stops drops during a burst and costs latency the whole
//     rest of the time. 4096 packets of 1400 bytes is 5.7 MB; on a 100 Mbit/s
//     link a full one is most of half a second of delay before a packet is even
//     sent. That delay is the jitter.
//   - Bigger socket buffers stop the kernel dropping datagrams the read loop
//     has not got to yet, and cost memory.
//
// The setting that resolves the first trade is not a number at all — it is the
// queueing discipline. fq_codel measures how long packets are actually waiting
// and drops when the delay climbs, so the queue can be deep enough to absorb a
// burst without becoming latency. Every preset here uses it, which is why the
// difference between them is memory rather than jitter.
//
// What is deliberately absent: anything resembling KCP's nodelay, resend or
// congestion knobs. A layer-3 tunnel carries whole IP packets, and those
// already belong to something that handles its own loss. Adding a retransmit
// timer underneath makes throughput collapse under loss rather than degrade,
// which is why the layer-3 engine has no KCP and no preset here can add one.

// l3Preset is one named set of tuning for a full IP tunnel.
type l3Preset struct {
	Name       string
	TxQueueLen int
	SockBuf    int
	Qdisc      string
}

// l3Presets are the choices, in the order they are offered.
var l3Presets = []l3Preset{
	{
		Name:       PresetBalance,
		TxQueueLen: 1024,
		SockBuf:    4 << 20,
		Qdisc:      "fq_codel",
	},
	{
		Name:       PresetTurbo,
		TxQueueLen: 4096,
		SockBuf:    8 << 20,
		Qdisc:      "fq_codel",
	},
	{
		Name: PresetAggressive,
		// Deep, and only useful with fq_codel in front of it: the queue is
		// there to absorb a burst on a fast link, and the discipline is what
		// stops it turning into latency the rest of the time.
		TxQueueLen: 16384,
		SockBuf:    32 << 20,
		Qdisc:      "fq_codel",
	},
}

// findL3Preset returns a preset by name, falling back to Turbo.
func findL3Preset(name string) l3Preset {
	for _, p := range l3Presets {
		if p.Name == name {
			return p
		}
	}
	return l3Presets[1]
}

// chooseL3Preset asks which one, described by what it costs and buys.
func chooseL3Preset() l3Preset {
	fmt.Println()
	tui.Info("These tune the queue between the kernel and the tunnel, and how much")
	tui.Info("room the carrier's sockets get. All three keep latency bounded with")
	tui.Info("fq_codel, so what really separates them is memory.")
	switch tui.ChooseOpt("How should the tunnel be tuned?", []tui.Option{
		{Title: "Turbo", Desc: "the default — 8 MB of socket buffer, suits most links. Start here"},
		{Title: "Balance", Desc: "smallest footprint, for a small VPS or several tunnels on one box"},
		{Title: "Aggressive", Desc: "for a fast link with bursts — 32 MB of buffer and a deep queue, wants RAM"},
	}) {
	case 1:
		return findL3Preset(PresetBalance)
	case 2:
		return findL3Preset(PresetAggressive)
	default:
		return findL3Preset(PresetTurbo)
	}
}

// apply copies a preset onto what the wizard collected.
func (p l3Preset) apply(s *l3Spec) {
	s.Preset = p.Name
	s.TxQueueLen = p.TxQueueLen
	s.SockBuf = p.SockBuf
	s.Qdisc = p.Qdisc
}
