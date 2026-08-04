//go:build !linux

package network

import (
	"errors"
	"net"
)

// The xdi transport is Linux-only: it needs a raw ICMP socket, which the rest
// of the code reaches through golang.org/x/net/icmp on Linux. These stubs let
// the package build on macOS and Windows for local development; a configuration
// that asks for xdi there is refused rather than silently doing something else.

var errXdiNotLinux = errors.New("the xdi transport is only available on Linux")

func newICMPServerConn(token string) (net.PacketConn, error) { return nil, errXdiNotLinux }
func newICMPClientConn(token string) (net.PacketConn, error) { return nil, errXdiNotLinux }

// icmpMTUOverhead mirrors the Linux value so the MTU arithmetic in kcp.go is
// identical on every platform, even though the socket is never opened here.
const icmpMTUOverhead = 8 + xdiHeaderLen
