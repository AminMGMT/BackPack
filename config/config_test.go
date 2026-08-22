package config

import "testing"

// The MTU default depends on whether FEC is on, because FEC is what makes a
// too-large MTU fatal: kcp-go pads every shard in a group out to the largest
// packet in it, so a FEC session offers a short path a steady stream of
// full-size packets and loses all of them, where a plain one slips its small
// packets through.
func TestKCPDefaultMTUAllowsForFEC(t *testing.T) {
	t.Run("a FEC session gets the conservative MTU", func(t *testing.T) {
		got := KCPConfig{DataShards: 10, ParityShards: 3}.WithDefaults()
		if got.MTU != 1250 {
			t.Errorf("MTU = %d, want 1250 with FEC on", got.MTU)
		}
	})

	t.Run("a session without FEC keeps the larger payload", func(t *testing.T) {
		got := KCPConfig{}.WithDefaults()
		if got.MTU != 1350 {
			t.Errorf("MTU = %d, want 1350 with FEC off", got.MTU)
		}
	})

	t.Run("half-configured FEC counts as off, for the MTU too", func(t *testing.T) {
		got := KCPConfig{DataShards: 10}.WithDefaults()
		if got.DataShards != 0 || got.ParityShards != 0 {
			t.Errorf("FEC = %d:%d, want it disabled", got.DataShards, got.ParityShards)
		}
		if got.MTU != 1350 {
			t.Errorf("MTU = %d, want 1350 when FEC resolved to off", got.MTU)
		}
	})

	t.Run("an MTU the config names is never overridden", func(t *testing.T) {
		got := KCPConfig{MTU: 1400, DataShards: 10, ParityShards: 3}.WithDefaults()
		if got.MTU != 1400 {
			t.Errorf("MTU = %d, want the configured 1400 left alone", got.MTU)
		}
	})
}
