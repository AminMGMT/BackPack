package spec

import (
	"fmt"
	"net"
	"strings"
)

// Where the tunnel's own control port listens.
//
// The forwarded ports have taken an address of their own for a while —
// "85.10.11.61:443=127.0.0.1:2053" binds that one address and nothing else.
// The control port could not: every path that produced a BindAddr wrote
// 0.0.0.0 (or :: when the operator asked for IPv6 as well) and joined the port
// onto it, so the tunnel always claimed every interface.
//
// On a single-homed server that is invisible. On one with two public addresses
// it is a wall: the operator who wants the control channel and a user-facing
// port to both be 443 — so the control channel looks like HTTPS too — cannot
// have it, because both end up asking for 0.0.0.0:443 and the second one gets
// `bind: address already in use`.
//
// Nothing in the engine needed changing for this. BindAddr is handed to
// net.Listen as it stands and has always accepted an address; the transports,
// the reload path's port settling, and portClash's host-aware comparison were
// all already written in terms of a full host:port. What was missing was a way
// to say it.
//
// So this is the one parser for what an operator types, and every entry point —
// the wizard, the CLI's edit screen, EditTunnel, and the panel's create and
// edit forms — goes through it.

// TunnelBind is a control port as the operator wrote it. Host is empty when
// they gave only a port, which is the case that has to keep meaning exactly
// what it meant before.
type TunnelBind struct {
	Host string
	Port string
}

// HasHost reports whether an address was named rather than left to the default.
func (b TunnelBind) HasHost() bool { return b.Host != "" }

// Addr renders the value for BindAddr. ipv6 decides the wildcard family only
// when no address was given: an operator who named one has already answered
// that question, and overriding it with a checkbox would be ignoring what they
// typed.
func (b TunnelBind) Addr(ipv6 bool) string {
	host := b.Host
	if host == "" {
		host = "0.0.0.0"
		if ipv6 {
			host = "::"
		}
	}
	return net.JoinHostPort(host, b.Port)
}

// ParseTunnelBind reads a control port in either of the two forms:
//
//	"443"                 every interface, exactly as before
//	"0.0.0.0:443"         the same thing, said out loud
//	"85.10.11.51:443"     that address only
//	"[::]:443"            every interface, v6 and v4 on a dual-stack host
//	"[2a01:4f8::1]:443"   that address only
//
// A host must be an IP literal. A name is refused rather than resolved: a bind
// address is a local interface, and a name that resolves to an address this
// machine does not have fails inside the listener with "cannot assign requested
// address" — an error that says nothing about the name that caused it.
func ParseTunnelBind(text string) (TunnelBind, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return TunnelBind{}, fmt.Errorf("no tunnel port given")
	}

	// A bare port is the common case and the one that has to stay cheap.
	if !strings.Contains(text, ":") {
		if !ValidPort(text) {
			return TunnelBind{}, fmt.Errorf("%q is not a port between 1 and 65535", text)
		}
		return TunnelBind{Port: text}, nil
	}

	host, port, err := net.SplitHostPort(text)
	if err != nil {
		// The message SplitHostPort gives for "2a01:4f8::1:443" is "too many
		// colons", which is true and unhelpful. Say what to write instead —
		// the same wording the forwarded-port parser uses, so an operator who
		// has met one has met both.
		if strings.Count(text, ":") > 1 && !strings.HasPrefix(text, "[") {
			return TunnelBind{}, fmt.Errorf("%q looks like an IPv6 address; write it as [address]:port", text)
		}
		return TunnelBind{}, fmt.Errorf("%q is not an address:port", text)
	}
	if !ValidPort(port) {
		return TunnelBind{}, fmt.Errorf("%q is not a port between 1 and 65535", port)
	}

	host = strings.TrimSpace(host)
	if host == "" {
		// ":443" — a port with the host left off, which is how a listen
		// address is spelled everywhere else in this program.
		return TunnelBind{Port: port}, nil
	}
	if net.ParseIP(host) == nil {
		return TunnelBind{}, fmt.Errorf("%q is not an IP address — the tunnel port binds a local "+
			"interface, so it takes an address this server holds (or just a port for all of them)", host)
	}
	return TunnelBind{Host: host, Port: port}, nil
}

// SplitBindAddr is ParseTunnelBind for a value already stored in a config,
// where the address has been through the parser once and is trusted. It exists
// so the two callers that need the host and the port apart do not each write
// their own SplitHostPort with their own idea of what a failure means.
func SplitBindAddr(addr string) TunnelBind {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return TunnelBind{}
	}
	if IsWildcardBind(host) {
		host = ""
	}
	return TunnelBind{Host: host, Port: port}
}

// BindHostOf is the address a server tunnel is pinned to, or "" when it listens
// on everything. It is what the panel shows back in the port field so the value
// an operator typed is the value they see next time.
func BindHostOf(addr string) string { return SplitBindAddr(addr).Host }

// LocalAddrExists reports whether an address is currently assigned to one of
// this machine's interfaces.
//
// Used to warn, never to refuse. A floating address, a VIP that keepalived has
// not claimed yet, or an interface that comes up after this runs are all
// legitimate reasons to configure an address the machine does not have at this
// instant — and net.ipv4.ip_nonlocal_bind exists precisely so that binding one
// can be made to work. Refusing would break those setups to catch a typo.
func LocalAddrExists(host string) bool {
	want := net.ParseIP(host)
	if want == nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return true // cannot tell; do not claim it is missing
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.Equal(want) {
			return true
		}
	}
	return false
}
