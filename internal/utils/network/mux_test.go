package network

import "testing"

// KCP and xDi cannot negotiate, so auto must resolve to the version the
// previous release used — 1 — or an upgraded end and a 1.6.5 end disagree and
// smux tears every session down. This is the regression that shipped a working
// tunnel that then failed to create any mux session.
func TestResolveStaticMuxVersion(t *testing.T) {
	for configured, want := range map[int]int{
		MuxVersionAuto: 1, // auto (0) → the historical KCP default, NOT 2
		1:              1,
		2:              2,
		7:              1, // out of range falls back to the safe default
	} {
		if got := ResolveStaticMuxVersion(configured); got != want {
			t.Errorf("ResolveStaticMuxVersion(%d) = %d, want %d", configured, got, want)
		}
	}

	// The value it returns must always be one smux accepts, whatever it is fed.
	for _, in := range []int{-5, 0, 1, 2, 3, 99} {
		if v := ResolveStaticMuxVersion(in); v != 1 && v != 2 {
			t.Fatalf("ResolveStaticMuxVersion(%d) = %d, which smux rejects", in, v)
		}
	}
}
