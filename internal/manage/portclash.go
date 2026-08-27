package manage

import (
	"fmt"
	"net"
	"strings"
)

// Two tunnels that cannot both work.
//
// A report: "I set one tunnel up, it works, I use it. Then I made a second one
// and it will not come up." Its log was an EOF on every handshake, forever, and
// one or two other people in the same chat had the same thing.
//
// Both ways of causing it are a second tunnel colliding with the first, and
// neither was refused when it was made:
//
//   - On the Iran side, two tunnels binding the same port. The second service
//     cannot bind and dies, which at least says so in its own log.
//   - On the kharej side, two tunnels dialling the same server and port. That
//     one is worse. Both start perfectly well, the server gives its single
//     control channel to whichever arrived first, and the second is refused for
//     as long as it runs — with no idea why, because a refusal used to be a
//     closed connection.
//
// The second is trivial to do by accident: copy the tunnel that works, change
// the name, and forget that the port belongs to the other end as much as to
// this one.
//
// So it is refused where it is cheap to fix — at the moment somebody types it —
// rather than discovered later from a log that says EOF.

// portClash reports why a new tunnel cannot coexist with the ones already
// configured, or "" when it can.
//
// addr is the new tunnel's bind address for a server, or the address it dials
// for a client. Comparison is on host and port together: two clients reaching
// two different servers on the same port number are not in conflict, and only
// the pair identifies a control channel.
func portClash(role, addr, newName string) string {
	return clashAgainst(role, addr, newName, List())
}

// clashAgainst is the decision, with no filesystem in it.
//
// Separated for the same reason directSettingsFrom is: ConfigDir is a constant
// and a test cannot point it somewhere safe, so the only way to check what this
// decides is to hand it the tunnels directly.
func clashAgainst(role, addr, newName string, existing []Tunnel) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "" // not a shape this can judge; the caller has already validated it
	}

	for _, t := range existing {
		if strings.EqualFold(t.Name, newName) || t.Role != role {
			continue
		}
		otherHost, otherPort, err := net.SplitHostPort(t.Addr)
		if err != nil || otherPort != port {
			continue
		}

		if role == "server" {
			// Two listeners, one port. Which of them binds it is a race the
			// operator did not ask to run.
			if sameBindHost(host, otherHost) {
				return fmt.Sprintf("tunnel %q already listens on port %s. Two tunnels "+
					"cannot share a port: whichever starts second fails to bind and stays "+
					"down. Give this one a port of its own.", t.Name, port)
			}
			continue
		}

		// A client. The pair is what identifies the far end's control channel,
		// so the same host and port is the same channel.
		if strings.EqualFold(otherHost, host) {
			return fmt.Sprintf("tunnel %q already connects to %s. A server hands out one "+
				"control channel, so the second tunnel to reach it is refused for as long "+
				"as it runs. Point this one at a different port on the server — and set "+
				"that same port up on the server side.", t.Name, addr)
		}
	}
	return ""
}

// sameBindHost reports whether two bind addresses would contend for a port.
//
// A wildcard covers everything, so it clashes with any other address on the same
// port — including the other wildcard family, since :: accepts IPv4 too on a
// dual-stack host, which is exactly why the setup form offers it as "IPv6 as
// well" rather than "IPv6 instead".
func sameBindHost(a, b string) bool {
	if isWildcardBind(a) || isWildcardBind(b) {
		return true
	}
	return strings.EqualFold(a, b)
}

func isWildcardBind(host string) bool {
	switch strings.Trim(host, "[]") {
	case "", "0.0.0.0", "::":
		return true
	}
	return false
}
