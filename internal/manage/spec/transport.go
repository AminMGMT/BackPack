package spec

import "strings"

// What a transport is, asked as a question about its name.
//
// These are the oldest predicates in the management layer and the most widely
// called: the wizard asks them to decide which questions to put, the panel asks
// them to decide which fields to draw, the health checks ask them to decide
// which probe means anything, and the renderers ask them to decide which keys
// to write. They are here because they are the bottom of all of that and they
// rest on nothing.

// IsMux reports whether a transport multiplexes several streams over one
// connection.
func IsMux(t string) bool {
	return t == "tcpmux" || t == "wsmux" || t == "wssmux" || t == "kcp" || t == "xdi" || t == "pck"
}

// IsKCP reports whether a transport is carried by KCP, and so takes the KCP
// tuning — the window sizes, the interval, the FEC shards.
func IsKCP(t string) bool {
	return t == "kcp" || t == "xdi" || t == "pck"
}

// IsDatagram reports whether a transport is carried in UDP datagrams. Such a
// tunnel never shows up in the TCP listen table and cannot be probed with a
// TCP connect, so every check that assumes TCP has to skip it.
func IsDatagram(t string) bool {
	// The two direct kinds carry their carrier in the name, so the bare
	// comparisons below never match them. Answering "no" for a layer-3 tunnel
	// was what made the web panel probe a pck tunnel with a TCP connect —
	// against a carrier that has no socket to connect to — and then read the
	// inevitable failure as the tunnel being down. The Iran card went offline
	// while the tunnel was carrying traffic and the kharej card stayed green,
	// because only the dialling side runs that probe.
	if strings.HasPrefix(t, "l3/") {
		// Every layer-3 carrier is a datagram one, and no reliable carrier can
		// ever be added — see the l3 package doc.
		return true
	}
	if strings.HasPrefix(t, "direct/") {
		// All four direct transports are stream transports over TCP, so a TCP
		// probe is exactly the right thing for them.
		return false
	}
	return t == "udp" || t == "kcp" || t == "xdi" || t == "quic" || t == "pck"
}

// SupportsProxyProtocol reports whether a transport can carry the PROXY
// protocol header that tells the service behind the tunnel the real client IP.
func SupportsProxyProtocol(t string) bool {
	switch t {
	case "tcp", "tcpmux", "kcp", "wsmux", "wssmux", "stealth", "quic", "pck":
		return true
	}
	return false
}

// IsWS reports whether a transport is a WebSocket one.
func IsWS(t string) bool {
	return t == "ws" || t == "wss" || t == "wsmux" || t == "wssmux"
}

// NeedsTLS reports whether a transport terminates TLS and so needs a
// certificate.
func NeedsTLS(t string) bool {
	return t == "wss" || t == "wssmux"
}

// ValidTransport reports whether a name is one of the reverse tunnel's
// carriers.
func ValidTransport(t string) bool {
	switch t {
	case "tcp", "tcpmux", "udp", "kcp", "ws", "wss", "wsmux", "wssmux", "stealth", "xdi", "quic", "pck":
		return true
	}
	return false
}
