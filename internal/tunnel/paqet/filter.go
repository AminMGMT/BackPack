package paqet

import (
	"errors"
	"fmt"
	"net"
)

// CaptureFilter is the BPF expression for the capture handle.
//
// The server captures everything addressed to its tunnel port: it does not know
// its clients. The client knows its one peer, so it takes only segments from
// that address and port to its own range of source ports, which keeps every
// unrelated flow on the interface out of the ring (and out of the CPU).
func CaptureFilter(server bool, lo, hi uint16, local, peer net.IP, peerPort uint16) string {
	dst := fmt.Sprintf("dst port %d", lo)
	if hi != lo {
		dst = fmt.Sprintf("dst portrange %d-%d", lo, hi)
	}
	f := "tcp and " + dst
	if local != nil {
		f += " and dst host " + local.String()
	}
	if !server && peer != nil {
		f += fmt.Sprintf(" and src host %s and src port %d", peer, peerPort)
	}
	return f
}

// ErrNoPcap is what OpenPcap answers in a build without libpcap.
var ErrNoPcap = errors.New("pck: this build of Backpack has no libpcap, so the packet carrier cannot run. " +
	"Install the official release binary (it is linked with libpcap), or build with CGO_ENABLED=1 and libpcap headers (see CONTRIBUTING.md)")
