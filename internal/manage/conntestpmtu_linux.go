//go:build linux

package manage

import (
	"net"
	"syscall"
)

// ctDontFragment makes a UDP socket send with DF set and never fragment
// locally, so a datagram too big for the path is lost (or refused with
// EMSGSIZE) rather than split — which is what a path-MTU probe measures.
func ctDontFragment(c *net.UDPConn) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := raw.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MTU_DISCOVER, syscall.IP_PMTUDISC_DO)
	}); err != nil {
		return err
	}
	return serr
}
