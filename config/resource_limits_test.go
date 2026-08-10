package config

import "testing"

func TestKCPDefaultsBoundAllocationInputs(t *testing.T) {
	k := (KCPConfig{
		MTU:          1 << 30,
		Resend:       100,
		SndWnd:       1 << 30,
		RcvWnd:       1 << 30,
		DataShards:   1 << 20,
		ParityShards: 1 << 20,
	}).WithDefaults()
	if k.MTU != maxKCPMTU || k.Resend != maxKCPResend || k.SndWnd != maxKCPWindow || k.RcvWnd != maxKCPWindow {
		t.Fatalf("KCP allocation inputs not bounded: %+v", k)
	}
	if k.DataShards != maxKCPShards || k.ParityShards != maxKCPShards {
		t.Fatalf("KCP FEC shards not bounded: %d/%d", k.DataShards, k.ParityShards)
	}
}

func TestKCPDefaultsPreserveValidTuning(t *testing.T) {
	want := KCPConfig{MTU: 1350, Interval: 10, Resend: 2, NoDelay: 1, NoCongestion: 1, SndWnd: 8192, RcvWnd: 8192, DataShards: 10, ParityShards: 3}
	if got := want.WithDefaults(); got != want {
		t.Fatalf("valid KCP tuning changed: got %+v want %+v", got, want)
	}
}
