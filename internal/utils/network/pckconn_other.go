//go:build !linux

package network

import (
	"fmt"
	"net"
)

// The pck carrier builds and reads raw frames through a packet socket, which is
// a Linux facility. Everything above it — KCP, the framing in pckframe.go, the
// management layer — is portable and compiles everywhere; only the socket is
// not, so this is where the platform stops.

// PcapCarrier is declared on every platform so configs and the management layer
// stay portable. See the Linux build for what each field does.
type PcapCarrier struct {
	Port       uint16
	Token      string
	Interface  string
	GatewayMAC string
	Flags      []TCPFlags
	PeerIP     string
}

// PckOverhead is what the pck framing costs inside the path MTU.
func PckOverhead() int { return pckOverhead }

func newPckConn(bool, uint16, PcapCarrier) (net.PacketConn, error) {
	return nil, fmt.Errorf("the pck transport needs a packet socket, which only Linux provides")
}
