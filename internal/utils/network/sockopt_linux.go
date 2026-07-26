//go:build linux

package network

import "golang.org/x/sys/unix"

// setCongestion asks the kernel to run this socket under the named congestion
// control algorithm.
//
// The Optimize step already sets BBR system-wide, but that can silently not be
// in effect: the sysctl fails on a kernel without the bbr module, a container
// may not permit it, an operator may never have run Optimize, and a reboot or
// another tool can put it back to cubic. Setting it per socket makes the
// tunnel's own connections use BBR whatever the system default happens to be.
//
// Best effort by design: a kernel that does not have the algorithm returns an
// error, and the connection is perfectly usable on whatever it falls back to —
// so the error is deliberately not propagated.
func setCongestion(fd uintptr, algo string) {
	_ = unix.SetsockoptString(int(fd), unix.IPPROTO_TCP, unix.TCP_CONGESTION, algo)
}
