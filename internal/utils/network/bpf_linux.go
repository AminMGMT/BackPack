//go:build linux

package network

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// attachSpoofBPF installs the tcp/icmp receive filter on the raw socket, so the
// kernel only delivers this tunnel's flow. Best effort: on any failure the
// caller keeps the socket and relies on the userspace tag check, which is why
// the error is advisory rather than fatal.
func attachSpoofBPF(pc net.PacketConn, profile SpoofProfile, port uint16, wantType byte) error {
	raw, err := spoofBPFProgram(profile, port, wantType)
	if err != nil {
		return err
	}
	filters := make([]unix.SockFilter, len(raw))
	for i, ins := range raw {
		filters[i] = unix.SockFilter{Code: ins.Op, Jt: ins.Jt, Jf: ins.Jf, K: ins.K}
	}
	fprog := &unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}

	ipc, ok := pc.(*net.IPConn)
	if !ok {
		return fmt.Errorf("spoof: raw receive socket is not an *net.IPConn")
	}
	sc, err := ipc.SyscallConn()
	if err != nil {
		return err
	}
	var setErr error
	if err := sc.Control(func(fd uintptr) {
		setErr = unix.SetsockoptSockFprog(int(fd), unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, fprog)
	}); err != nil {
		return err
	}
	return setErr
}
