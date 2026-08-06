//go:build linux

package network

import (
	"net"

	"golang.org/x/sys/unix"
)

// bindPacketConnToInterface pins the raw spoof socket to a named device,
// reaching its file descriptor through the syscall control interface. The raw
// socket is the *net.IPConn that net.ListenPacket("ip4:proto", …) returns.
// Needs CAP_NET_RAW.
func bindPacketConnToInterface(pc net.PacketConn, name string) error {
	ipc, ok := pc.(*net.IPConn)
	if !ok {
		return unix.EINVAL
	}
	raw, err := ipc.SyscallConn()
	if err != nil {
		return err
	}
	var ctrlErr error
	if err := raw.Control(func(fd uintptr) { ctrlErr = bindToInterface(fd, name) }); err != nil {
		return err
	}
	return ctrlErr
}

// bindToInterface pins a socket to a named device, so its traffic leaves by
// that link whatever the routing table would otherwise have chosen.
//
// It needs CAP_NET_RAW. A tunnel running as root has it; one that does not will
// get EPERM here, and that is reported rather than swallowed — an operator who
// named an interface is entitled to know the socket did not end up on it.
func bindToInterface(fd uintptr, name string) error {
	return unix.BindToDevice(int(fd), name)
}

// setFirewallMark stamps an fwmark on the socket's packets. It is what `ip rule`
// matches on, and therefore how a connection is put onto a routing table of its
// own without touching routing for the rest of the machine.
//
// It needs CAP_NET_ADMIN.
func setFirewallMark(fd uintptr, mark int) error {
	return unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, mark)
}
