package l3

import (
	"testing"

	"github.com/backpack/backpack/config"
)

// The spoof carrier has two keys for one setting and used to read the wrong one.
//
// spoof_sockbuf is what the panel's Advanced form collects and what
// directrender writes; sockbuf is the [l3] table's own key, which every other
// carrier reads. openSpoof passed cfg.SockBuf, so the specific key was
// validated on the way in, written to the file, and read back into the form on
// the next visit — while the socket got the general one or the 4 MiB default.
// A setting that reads back as applied and is not is worse than one that does
// not exist, because it gets ruled out as a cause.
func TestTheSpoofCarrierPrefersItsOwnSocketBuffer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		general  int
		specific int
		want     int
	}{
		{"neither set leaves it to the carrier default", 0, 0, 0},
		{"only the general key, which is how it always behaved", 1 << 20, 0, 1 << 20},
		{"only the specific key, which used to be ignored", 0, 8 << 20, 8 << 20},
		{"both set: the one naming this carrier wins", 1 << 20, 8 << 20, 8 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{SockBuf: tc.general}
			cfg.Spoof = config.SpoofConfig{SpoofSockBuf: tc.specific}
			if got := spoofSockBuf(cfg); got != tc.want {
				t.Errorf("spoofSockBuf() = %d, want %d", got, tc.want)
			}
		})
	}
}
