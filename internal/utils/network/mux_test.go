package network

import "testing"

// Auto has to resolve to 2, because it is only ever consulted on a handshake
// the peer had to be new to speak — and a new peer does as it is told. An
// explicit choice is left alone.
func TestResolveMuxVersion(t *testing.T) {
	for configured, want := range map[int]int{
		MuxVersionAuto: 2,
		1:              1,
		2:              2,
		// Out of range never reaches here (applyDefaults resets it), but
		// falling through to the negotiated answer is the safe reading.
		7:  2,
		-1: 2,
	} {
		if got := ResolveMuxVersion(configured); got != want {
			t.Errorf("ResolveMuxVersion(%d) = %d, want %d", configured, got, want)
		}
	}
}

// The version has to reach the smux config unchanged. Everything else about
// this scheme rests on the two ends building sessions on the same number, and
// smux tears down any session where they do not.
func TestSmuxConfigCarriesTheVersionAndTuning(t *testing.T) {
	s := MuxSettings{MaxFrameSize: 4096, MaxReceiveBuffer: 1 << 20, MaxStreamBuffer: 1 << 16}

	for _, version := range []int{1, 2} {
		cfg := SmuxConfig(version, s)
		if cfg.Version != version {
			t.Errorf("SmuxConfig(%d) built version %d", version, cfg.Version)
		}
		if cfg.MaxFrameSize != s.MaxFrameSize {
			t.Errorf("frame size %d, want %d", cfg.MaxFrameSize, s.MaxFrameSize)
		}
		if cfg.MaxReceiveBuffer != s.MaxReceiveBuffer {
			t.Errorf("receive buffer %d, want %d", cfg.MaxReceiveBuffer, s.MaxReceiveBuffer)
		}
		if cfg.MaxStreamBuffer != s.MaxStreamBuffer {
			t.Errorf("stream buffer %d, want %d", cfg.MaxStreamBuffer, s.MaxStreamBuffer)
		}
		// smux validates its own config on use; a version it rejects would
		// surface only when a session was built, which is far too late.
		if cfg.KeepAliveTimeout <= cfg.KeepAliveInterval {
			t.Error("keepalive timeout must exceed the interval or smux refuses the config")
		}
	}
}
