//go:build !linux

package network

import (
	"errors"
	"net"
)

// The spoof transport is Linux-only: it needs a raw IPv4 socket with the header
// under its control, which the rest of the code reaches through
// golang.org/x/net/ipv4 on Linux. These stubs let the package build on macOS and
// Windows for local development; a configuration that asks for spoof there is
// refused rather than silently doing something else.

var errSpoofNotLinux = errors.New("the spoof transport is only available on Linux")

func newSpoofServerConn(o spoofConnOpts) (net.PacketConn, error) {
	return nil, errSpoofNotLinux
}

func newSpoofClientConn(o spoofConnOpts) (net.PacketConn, error) {
	return nil, errSpoofNotLinux
}

// SpoofRawSender is the spoof tester's send half; Linux only. The stub lets the
// tester package build on other platforms, where NewSpoofRawSender is refused.
type SpoofRawSender struct{}

func NewSpoofRawSender(iface string) (*SpoofRawSender, error) { return nil, errSpoofNotLinux }
func (s *SpoofRawSender) SendUDP(src, dst net.IP, srcPort, dstPort uint16, payload []byte) error {
	return errSpoofNotLinux
}
func (s *SpoofRawSender) Close() error { return nil }

// spoofOverhead mirrors the Linux value so the MTU arithmetic in kcp.go is
// identical on every platform, even though the socket is never opened here.
func spoofOverhead(p SpoofProfile) int {
	return 20 + profileL4Len(p) + xdiHeaderLen
}
