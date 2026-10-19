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
	level, option, value := syscall.IPPROTO_IP, syscall.IP_MTU_DISCOVER, syscall.IP_PMTUDISC_DO
	if addr, ok := c.LocalAddr().(*net.UDPAddr); ok && addr.IP.To4() == nil {
		level, option, value = syscall.IPPROTO_IPV6, syscall.IPV6_MTU_DISCOVER, syscall.IPV6_PMTUDISC_DO
	}
	if err := raw.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), level, option, value)
	}); err != nil {
		return err
	}
	return serr
}
