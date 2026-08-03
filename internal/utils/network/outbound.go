package network

import (
	"fmt"
	"net"
	"strings"
	"syscall"
)

// Choosing the way out of a multi-homed machine.
//
// A server with one uplink needs none of this: there is one route out and the
// kernel takes it. A server with two is a different question, and it is a
// question the tunnel could not answer. Which uplink the tunnel leaves by is
// exactly the sort of thing an operator needs to decide — one link is metered
// and the other is not, one is filtered and the other is not, one is the
// business line and the other is the cheap one bought to route around a block.
// Without a say in it the kernel's routing table decides, and the only way to
// influence that was to change routing for the whole machine.
//
// Three ways to say it, in increasing order of what they need from the system:
//
//   - LocalAddr binds the source address. It needs no privilege at all and
//     works everywhere, and on a host whose uplinks have distinct addresses it
//     is usually the whole answer.
//   - Interface pins the socket to a named device, which is the answer when the
//     address alone does not settle it. Linux, and it needs CAP_NET_RAW.
//   - Mark stamps an fwmark on the packets, which is what `ip rule` matches on.
//     It is how a socket is put onto a routing table of its own. Linux, and it
//     needs CAP_NET_ADMIN.
//
// All three fail the dial when they are configured and cannot be applied, which
// is the opposite of how the tunnel treats its buffer sizes and its congestion
// control. Those are tuning nobody asked for by name, and a kernel that refuses
// them leaves a connection that works. These were asked for by name, and a
// tunnel that quietly ignored "leave by eth1" would send traffic out the wrong
// link while reporting itself healthy — which is the failure this whole area of
// the code has spent too long producing.
//
// And, as with the proxy, none of it reaches the dial to the local backend.
// That connection does not leave the machine, so pinning it to an external
// interface or source address would simply break it.

// Outbound describes how a tunnel connection leaves this machine. A nil
// *Outbound is the ordinary case: dial directly, and let the kernel route.
type Outbound struct {
	// Proxy routes the connection through a SOCKS5 or HTTP proxy.
	Proxy *ProxyConfig
	// LocalAddr is the source address to bind, as host or host:port. A bare
	// address gets port 0, so the kernel still picks the port.
	LocalAddr string
	// Interface is the device name to pin the socket to (Linux).
	Interface string
	// Mark is the fwmark to stamp on the packets, 0 for none (Linux).
	Mark int
}

// localTCPAddr resolves the configured source address, or nil when there is
// none.
func (o *Outbound) localTCPAddr() (*net.TCPAddr, error) {
	if o == nil || o.LocalAddr == "" {
		return nil, nil
	}
	addr := o.LocalAddr
	// A bare address is the usual way to write this; the port is the kernel's
	// to choose, so it is filled in rather than demanded.
	if _, _, err := net.SplitHostPort(addr); err != nil {
		// An IPv6 address may already be bracketed, and JoinHostPort brackets
		// whatever it is given that contains a colon — so a written "[::1]"
		// would come back as "[[::1]]:0" and resolve to nothing.
		host := addr
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
		addr = net.JoinHostPort(host, "0")
	}
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("local_addr %q: %w", o.LocalAddr, err)
	}
	return tcpAddr, nil
}

// control applies the options that live on the socket itself. It runs inside
// the dialer's Control callback, before connect.
func (o *Outbound) control(fd uintptr) error {
	if o == nil {
		return nil
	}
	if o.Interface != "" {
		if err := bindToInterface(fd, o.Interface); err != nil {
			return fmt.Errorf("could not bind the connection to interface %q: %w", o.Interface, err)
		}
	}
	if o.Mark != 0 {
		if err := setFirewallMark(fd, o.Mark); err != nil {
			return fmt.Errorf("could not set fwmark %d on the connection: %w", o.Mark, err)
		}
	}
	return nil
}

// dialAddress is where the socket actually connects: the proxy when there is
// one, and the server itself otherwise.
func (o *Outbound) dialAddress(remote string) string {
	if o != nil && o.Proxy != nil {
		return o.Proxy.Address
	}
	return remote
}

// proxy returns the configured proxy, or nil.
func (o *Outbound) proxy() *ProxyConfig {
	if o == nil {
		return nil
	}
	return o.Proxy
}

// IsSet reports whether anything at all is configured, so a caller can skip
// building one.
func (o *Outbound) IsSet() bool {
	return o != nil && (o.Proxy != nil || o.LocalAddr != "" || o.Interface != "" || o.Mark != 0)
}

// String renders the binding for a log line.
func (o *Outbound) String() string {
	if !o.IsSet() {
		return "direct"
	}
	var parts []string
	if o.Proxy != nil {
		parts = append(parts, "via "+o.Proxy.String())
	}
	if o.LocalAddr != "" {
		parts = append(parts, "from "+o.LocalAddr)
	}
	if o.Interface != "" {
		parts = append(parts, "on "+o.Interface)
	}
	if o.Mark != 0 {
		parts = append(parts, fmt.Sprintf("fwmark %d", o.Mark))
	}
	return strings.Join(parts, ", ")
}

// Validate checks what can be checked before anything is dialled, so a name
// that does not exist is reported against the line of the config that holds it
// rather than as a connection that never succeeds.
func (o *Outbound) Validate() error {
	if o == nil {
		return nil
	}
	if _, err := o.localTCPAddr(); err != nil {
		return err
	}
	if o.Interface != "" {
		if _, err := net.InterfaceByName(o.Interface); err != nil {
			return fmt.Errorf("interface %q: %w", o.Interface, err)
		}
	}
	if o.Mark < 0 {
		return fmt.Errorf("so_mark must not be negative, got %d", o.Mark)
	}
	return nil
}

// outboundControl adapts control to the signature net.Dialer wants.
func (o *Outbound) outboundControl(_, _ string, c syscall.RawConn) error {
	var applyErr error
	if err := c.Control(func(fd uintptr) { applyErr = o.control(fd) }); err != nil {
		return err
	}
	return applyErr
}
