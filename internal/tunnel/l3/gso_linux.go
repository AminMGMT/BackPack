//go:build linux

package l3

import (
	"encoding/binary"
	"errors"
	"net"
	"unsafe"

	"golang.org/x/sys/unix"
)

// errNoGSO says this socket will not take a segmented write. It is a condition
// the caller falls back from, not a failure to report.
var errNoGSO = errors.New("l3: this carrier cannot segment a write")

// writeGSO sends a whole batch as one segmented write.
//
// It reports how many datagrams the kernel took. A refusal comes back as
// errNoGSO with nothing sent, so the caller can send the same batch the old way
// without losing any of it — see gso.go for why that is a condition rather than
// an error.
func (c *udpCarrier) writeGSO(bufs [][]byte, to net.Addr) (int, error) {
	if c.UDPConn == nil || c.gsoOff {
		return 0, errNoGSO
	}
	segment, ok := gsoEligible(bufs)
	if !ok {
		return 0, errNoGSO
	}
	sa, err := sockaddrOf(to)
	if err != nil {
		return 0, errNoGSO
	}

	// One buffer for the run, grown as needed and kept. It is held under the
	// send lock the caller already owns.
	total := 0
	for _, b := range bufs {
		total += len(b)
	}
	if cap(c.gsoBuf) < total {
		c.gsoBuf = make([]byte, total)
	}
	run := c.gsoBuf[:0]
	for _, b := range bufs {
		run = append(run, b...)
	}

	if c.gsoCmsg == nil {
		c.gsoCmsg = make([]byte, unix.CmsgSpace(2))
		h := (*unix.Cmsghdr)(unsafe.Pointer(&c.gsoCmsg[0]))
		h.Level = unix.IPPROTO_UDP
		h.Type = unix.UDP_SEGMENT
		h.SetLen(unix.CmsgLen(2))
	}
	binary.NativeEndian.PutUint16(c.gsoCmsg[unix.CmsgLen(0):], uint16(segment))

	raw, err := c.UDPConn.SyscallConn()
	if err != nil {
		return 0, errNoGSO
	}
	var opErr error
	if err := raw.Control(func(fd uintptr) {
		_, opErr = unix.SendmsgN(int(fd), run, c.gsoCmsg, sa, 0)
	}); err != nil {
		return 0, errNoGSO
	}
	if opErr != nil {
		// A socket buffer that is momentarily full is not a reason to give the
		// feature up for ever — it says nothing about whether the kernel
		// supports segmenting. Everything else does.
		if errors.Is(opErr, unix.EAGAIN) || errors.Is(opErr, unix.EWOULDBLOCK) ||
			errors.Is(opErr, unix.ENOBUFS) || errors.Is(opErr, unix.EINTR) {
			return 0, errNoGSO
		}
		c.gsoOff = true
		return 0, errNoGSO
	}
	return len(bufs), nil
}

// sockaddrOf converts a destination to the form a raw send call wants.
func sockaddrOf(to net.Addr) (unix.Sockaddr, error) {
	ua, ok := to.(*net.UDPAddr)
	if !ok || ua.IP == nil {
		return nil, errNoGSO
	}
	if ip4 := ua.IP.To4(); ip4 != nil {
		sa := &unix.SockaddrInet4{Port: ua.Port}
		copy(sa.Addr[:], ip4)
		return sa, nil
	}
	sa := &unix.SockaddrInet6{Port: ua.Port}
	copy(sa.Addr[:], ua.IP.To16())
	if ua.Zone != "" {
		if iface, err := net.InterfaceByName(ua.Zone); err == nil {
			sa.ZoneId = uint32(iface.Index)
		}
	}
	return sa, nil
}
