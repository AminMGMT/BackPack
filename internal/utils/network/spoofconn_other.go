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

func newSpoofServerConn(token string, profile SpoofProfile, spoofSrcIP, iface string) (net.PacketConn, error) {
	return nil, errSpoofNotLinux
}

func newSpoofClientConn(token string, profile SpoofProfile, spoofSrcIP, iface string) (net.PacketConn, error) {
	return nil, errSpoofNotLinux
}

// spoofOverhead mirrors the Linux value so the MTU arithmetic in kcp.go is
// identical on every platform, even though the socket is never opened here.
func spoofOverhead(p SpoofProfile) int {
	l4 := 8
	if p == SpoofProfileTCP {
		l4 = 20
	}
	return 20 + l4 + xdiHeaderLen
}
