//go:build !linux

package l3

import (
	"errors"
	"net"
)

// errNoGSO says this socket will not take a segmented write, which off Linux is
// always: UDP_SEGMENT is a Linux socket option.
var errNoGSO = errors.New("l3: segmented writes are only available on Linux")

func (c *udpCarrier) writeGSO([][]byte, net.Addr) (int, error) { return 0, errNoGSO }
