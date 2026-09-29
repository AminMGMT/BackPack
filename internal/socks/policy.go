package socks

import (
	"errors"
	"net"
	"syscall"
	"time"
)

// Where the proxy may connect to.
//
// A proxy on a node's exit side is reached through a forwarded port, and a
// forwarded port on the Iran server is open to anyone who finds it: the tunnel
// token proves the two servers to each other, not the people using the ports.
// So whatever the proxy will connect to, it will connect to for strangers — and
// on the machine it runs on that includes its own loopback services (the
// panel, a debug endpoint) and the cloud provider's metadata address, which
// hand out what nobody outside was meant to see. Those are refused whatever
// the proxy's own authentication, checked against the address actually being
// connected to, after the name has been resolved, so a name that resolves to
// one of them is refused too.

// Target reports whether the proxy may connect to ip. A variable so a test,
// which has nothing but loopback to connect to, can widen it.
var Target = PublicOrPrivate

// PublicOrPrivate refuses this machine's own addresses and the link-local
// range the cloud metadata services answer on; anything routable, public or
// private, is allowed.
func PublicOrPrivate(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast())
}

// ErrRefusedTarget is a destination Target refused.
var ErrRefusedTarget = errors.New("socks: that destination is refused: it is this machine, or link-local")

// control refuses a connection whose resolved address Target does not allow.
func control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrRefusedTarget
	}
	if ip := net.ParseIP(host); ip == nil || !Target(ip) {
		return ErrRefusedTarget
	}
	return nil
}

// DialTarget connects to a proxy destination under Target.
func DialTarget(network, addr string, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout, Control: control}
	return d.Dial(network, addr)
}
