package manage

import (
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/tunnel/l3"
)

// Every preset must survive the round trip and be one the engine accepts.
func TestL3PresetsReachTheEngine(t *testing.T) {
	for _, p := range l3Presets {
		spec := l3Spec{
			Side: sideIran, Carrier: "pck", Encap: "gre",
			Addr: "1.2.3.4:9000", Token: "t", Iface: "bp0",
			LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1371,
		}
		p.apply(&spec)
		cfg := decode(t, spec.render())

		if cfg.L3.Preset != p.Name {
			t.Errorf("%s: preset round trip = %q", p.Name, cfg.L3.Preset)
		}
		if cfg.L3.TxQueueLen != p.TxQueueLen {
			t.Errorf("%s: txqueuelen = %d, want %d", p.Name, cfg.L3.TxQueueLen, p.TxQueueLen)
		}
		if cfg.L3.SockBuf != p.SockBuf {
			t.Errorf("%s: sockbuf = %d, want %d", p.Name, cfg.L3.SockBuf, p.SockBuf)
		}

		engine := l3.Config{
			Mode: "dial", Addr: cfg.L3.Addr, Token: cfg.L3.Token,
			Carrier: cfg.L3.Carrier, Encap: cfg.L3.Encap,
			Iface: cfg.L3.Iface, LocalIP: cfg.L3.LocalIP, PeerIP: cfg.L3.PeerIP,
			MTU: cfg.L3.MTU, SockBuf: cfg.L3.SockBuf,
			TxQueueLen: cfg.L3.TxQueueLen, Qdisc: cfg.L3.Qdisc,
		}
		if err := engine.Validate(); err != nil {
			t.Errorf("%s: the engine refused a preset-written config: %v", p.Name, err)
		}
	}
}

// Every preset must use an active queue discipline.
//
// It is the one setting that decides jitter, and the reason a deep queue is
// affordable at all: fq_codel drops when packets start waiting, so the sender
// backs off before the queue turns into delay. A preset that left plain fq —
// or nothing — would hand its user bufferbloat and call it a speed profile.
func TestEveryL3PresetKeepsLatencyBounded(t *testing.T) {
	for _, p := range l3Presets {
		if p.Qdisc != "fq_codel" {
			t.Errorf("preset %q queues with %q, which does not manage delay", p.Name, p.Qdisc)
		}
		if p.TxQueueLen <= 0 || p.SockBuf <= 0 {
			t.Errorf("preset %q leaves a value unset: queue=%d sockbuf=%d",
				p.Name, p.TxQueueLen, p.SockBuf)
		}
	}
}

// The three must actually differ, in the direction their names promise.
func TestL3PresetsAreOrdered(t *testing.T) {
	balance := findL3Preset(PresetBalance)
	turbo := findL3Preset(PresetTurbo)
	aggressive := findL3Preset(PresetAggressive)

	if !(balance.SockBuf < turbo.SockBuf && turbo.SockBuf < aggressive.SockBuf) {
		t.Errorf("socket buffers are not ordered: %d, %d, %d",
			balance.SockBuf, turbo.SockBuf, aggressive.SockBuf)
	}
	if !(balance.TxQueueLen < turbo.TxQueueLen && turbo.TxQueueLen < aggressive.TxQueueLen) {
		t.Errorf("queues are not ordered: %d, %d, %d",
			balance.TxQueueLen, turbo.TxQueueLen, aggressive.TxQueueLen)
	}
	// An unknown name must not fail; it falls back to the middle one.
	if findL3Preset("nonsense").Name != PresetTurbo {
		t.Error("an unknown preset name does not fall back to Turbo")
	}
}

// A layer-3 preset must not carry KCP tuning. Stacking a retransmit timer under
// IP makes throughput collapse under loss rather than degrade, which is why the
// engine has no KCP — a preset that set one would be configuring nothing, and
// implying something false about how the tunnel works.
func TestL3PresetsCarryNoKCPTuning(t *testing.T) {
	spec := l3Spec{
		Side: sideIran, Carrier: "pck", Encap: "gre", Addr: "1.2.3.4:9000",
		Token: "t", Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1371,
	}
	findL3Preset(PresetAggressive).apply(&spec)
	body := spec.render()

	for _, key := range []string{"nodelay", "resend", "nocongestion", "interval", "sndwnd", "rcvwnd", "fec"} {
		if strings.Contains(body, key) {
			t.Errorf("a layer-3 config carries the KCP key %q, which its engine does not read:\n%s", key, body)
		}
	}
}
