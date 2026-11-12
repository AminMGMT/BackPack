//go:build !(linux && cgo)

package paqet

import "github.com/backpack/backpack/internal/utils/network"

// PcapAvailable is false in a build without cgo and libpcap (or off Linux).
// Such a build compiles and passes the engine's tests, which use a fake wire,
// but cannot open a real capture. Releases are never built this way: see
// CONTRIBUTING.md and the Makefile.
const PcapAvailable = false

// MaxSenders bounds the send handles of one engine.
const MaxSenders = 4

// OpenPcap is unavailable without libpcap.
func OpenPcap(*network.PckPath, string, int, []network.TCPFlags, int) (Capture, Injector, error) {
	return nil, nil, ErrNoPcap
}
