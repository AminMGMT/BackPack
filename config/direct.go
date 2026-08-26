package config

import "strings"

// DirectConfig is a direct tunnel: the same forwarded ports the reverse tunnel
// serves, with the tunnel dialled the other way round.
//
// In the reverse tunnel the Iran server listens and kharej dials in. Where
// that inbound connection cannot be made — a provider that filters connections
// arriving from abroad, a port blocked in one direction only — the tunnel
// cannot come up at all, even though the user-facing ports on Iran are fine.
// Here Iran dials out instead, which is the ordinary direction and the one a
// filter is least likely to touch. The ports do not move: Iran still exposes
// them and kharej still holds the real service.
//
// Like [l3], this lives in its own table and shares nothing with [server] and
// [client]. A file without a [direct] table cannot reach this code, and one
// with it never reaches the reverse engine.
//
//	# Iran — dials out, needs no inbound port of its own
//	[direct]
//	role  = "iran"
//	addr  = "KHAREJ_IP:8443"
//	token = "SAME_LONG_TOKEN"
//	ports = ["443", "2053-2060"]
//
//	# Kharej — listens
//	[direct]
//	role  = "kharej"
//	addr  = "0.0.0.0:8443"
//	token = "SAME_LONG_TOKEN"
type DirectConfig struct {
	// Role is "iran" or "kharej". Empty means there is no direct tunnel in
	// this configuration, which is how every pre-existing config reads.
	//
	// The names are geographic because that is what stays true across both
	// directions: Iran exposes the ports either way, and only who dials
	// changes. "edge" and "origin" are accepted as synonyms, which is what the
	// engine calls them internally.
	Role string `toml:"role"`

	// Addr is the kharej server's host:port on the Iran side, or the address
	// to bind on the kharej side.
	Addr string `toml:"addr"`

	// Token is the shared secret. It never travels on the wire: each end
	// proves it holds the token with an HMAC over two fresh nonces.
	Token string `toml:"token"`

	// Transport is "tcp" (plain, for a payload that is already TLS),
	// "stealth" (the Noise record layer, with no handshake or fingerprint for
	// inspection to match), or "ws"/"wss" (an HTTP upgrade in front of the
	// stream, which is what a CDN will proxy). Empty means "tcp".
	Transport string `toml:"transport"`

	// ServerName is the name the Iran side presents in SNI and the Host
	// header on ws and wss — the domain in front of a CDN. Empty uses the host
	// of Addr and, on wss, skips certificate verification: the tunnel
	// authenticates on the token rather than the certificate, so a
	// self-signed origin is the expected case. Setting it turns verification
	// on.
	ServerName string `toml:"server_name"`

	// TLSCertFile and TLSKeyFile are the kharej side's certificate for wss.
	// Both empty generates a self-signed one, which is what a direct
	// connection to an IP address wants.
	TLSCertFile string `toml:"tls_cert"`
	TLSKeyFile  string `toml:"tls_key"`

	// ACMEDomain switches the kharej side to a Let's Encrypt certificate for
	// that domain instead of a generated one. The domain must resolve to it.
	ACMEDomain string `toml:"acme_domain"`
	ACMEEmail  string `toml:"acme_email"`

	// Ports are the forwarded port mappings, served on the Iran side, in the
	// same syntax the reverse tunnel uses. A target with no host of its own
	// means the loopback of the kharej machine, where the real service
	// listens. The kharej side needs none: every target arrives on the stream
	// that asks for it.
	Ports []string `toml:"ports"`

	// AcceptUDP forwards UDP as well as TCP on those ports. Off unless set,
	// matching the reverse tunnel: a tunnel should not silently start carrying
	// every QUIC flow on port 443.
	AcceptUDP bool `toml:"accept_udp"`

	// MaxConnections caps how many forwarded connections may be open at once
	// (0 = unlimited), and BandwidthMbps caps total throughput in Mbit/s
	// (0 = unlimited). Both are enforced on the Iran side, where the users
	// arrive, and both are off unless set.
	MaxConnections int `toml:"max_connections"`
	BandwidthMbps  int `toml:"bandwidth_mbps"`

	// Preset is the name of the tuning profile the keys below came from. It is
	// a label: the engine reads the individual values, not this. It exists so
	// a management screen can say "Turbo" rather than reciting four numbers.
	Preset string `toml:"preset"`

	// Sessions is how many mux sessions the Iran side keeps open, with new
	// connections spread across them. One is enough for most traffic; more
	// helps where a single connection is being shaped.
	Sessions int `toml:"sessions"`

	// DialTimeout bounds a dial, in seconds. Zero takes the default.
	DialTimeout int `toml:"dial_timeout"`

	// RetryInterval is how long to wait before redialling a dropped session,
	// in seconds. Zero takes the default.
	RetryInterval int `toml:"retry_interval"`

	// Keepalive is the TCP keepalive period on the tunnel connection, in
	// seconds.
	Keepalive int `toml:"keepalive_period"`

	// Nodelay disables Nagle on the tunnel connection.
	Nodelay bool `toml:"nodelay"`

	// MSS clamps the largest TCP payload this end puts in one segment on the
	// tunnel connection. Zero — the default — leaves the decision to the
	// kernel.
	//
	// It is the same key, and the same fix, the reverse tunnel takes: where a
	// path carries less than a full-sized packet and drops the oversized ones
	// without an ICMP reply, nothing on either machine learns. The handshake
	// and the mux's keepalives are small enough to arrive, so the tunnel comes
	// up and looks healthy while every real transfer stalls on the first full
	// segment — and because the socket stays ESTABLISHED throughout, the
	// watchdog sees nothing wrong either.
	//
	// Direct was the only one of the three tunnel kinds with no way to set
	// this. The reverse tunnel has had it since the same failure was diagnosed
	// there, and [l3] measures the path itself.
	//
	// It has to be set at both ends: each end clamps only what it sends.
	MSS int `toml:"mss"`

	// MuxVersion, MaxFrameSize, MaxReceiveBuffer and MaxStreamBuffer tune the
	// mux session. They are the same keys the reverse mux transports take.
	MuxVersion       int `toml:"mux_version"`
	MaxFrameSize     int `toml:"mux_framesize"`
	MaxReceiveBuffer int `toml:"mux_recievebuffer"`
	MaxStreamBuffer  int `toml:"mux_streambuffer"`
}

// Enabled reports whether this configuration describes a direct tunnel. It is
// the single gate between this engine and the others: false — what every
// configuration written before this existed returns — means nothing in the
// direct path is reachable.
func (d DirectConfig) Enabled() bool {
	return strings.TrimSpace(d.Role) != ""
}

// ResolvedRole maps what an operator writes onto what the engine calls it.
// Geography is the user-facing name because it does not change with the
// direction; edge and origin are what the code uses, and are accepted here so
// a config written either way works.
func (d DirectConfig) ResolvedRole() string {
	switch strings.ToLower(strings.TrimSpace(d.Role)) {
	case "iran", "edge":
		return "edge"
	case "kharej", "origin":
		return "origin"
	default:
		// Passed through unchanged so the engine's own validation produces the
		// error, with the list of what it accepts.
		return strings.ToLower(strings.TrimSpace(d.Role))
	}
}
