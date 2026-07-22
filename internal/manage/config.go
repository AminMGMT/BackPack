package manage

import (
	"fmt"
	"os"
	"strings"

	"github.com/backpack/backpack/internal/app"
)

// TunnelSpec is the full description of a tunnel used to render a TOML config.
type TunnelSpec struct {
	Name      string
	Role      string // "server" (Iran/edge that exposes ports) or "client" (kharej/origin)
	Transport string // tcp, tcpmux, udp, kcp, ws, wss, wsmux, wssmux

	// Preset is the performance profile every tuning field was filled from:
	// balance, turbo or aggressive. Empty means the values were set by hand or
	// by a version that predates presets — either way they are left untouched.
	Preset string

	BindAddr   string   // server: listen address for the tunnel control channel
	RemoteAddr string   // client: address of the server tunnel port
	Token      string   // shared secret
	Ports      []string // server: exposed/forwarded ports

	// LoadBalance spreads data connections across FallbackAddrs instead of
	// only using them when the primary fails. Every address must reach the
	// same server.
	LoadBalance bool

	// FallbackAddrs are extra server addresses a client tries, in order, when
	// the primary one cannot be reached — a second IP, a different port, or a
	// CDN edge. This keeps the tunnel up when one address gets filtered.
	FallbackAddrs []string

	Nodelay        bool
	Heartbeat      int
	KeepAlive      int
	ChannelSize    int
	ConnectionPool int
	AggressivePool bool
	AcceptUDP      bool
	LogLevel       string
	// LogFormat is "" for human-readable output or "json" for machine parsing.
	LogFormat string

	// SMUX / multiplexed transports
	MuxCon          int
	MuxVersion      int
	MuxFrameSize    int
	MuxRecvBuffer   int
	MuxStreamBuffer int

	// KCP transport (reliable ARQ over UDP). Filled from the preset; only
	// written to the config when the transport is kcp.
	KCPMTU          int
	KCPInterval     int // ARQ tick in milliseconds — lower reacts faster, costs CPU
	KCPResend       int // fast-retransmit threshold in duplicate ACKs
	KCPNoDelay      int // 1 enables the low-latency ARQ mode
	KCPNoCongestion int // 1 disables KCP's own congestion window
	KCPSndWnd       int // send window in packets
	KCPRcvWnd       int // receive window in packets
	KCPAckNoDelay   bool
	// FEC: every KCPDataShards packets carry KCPParityShards parity packets, so
	// that many losses are repaired without waiting for a retransmit. 0 = off.
	KCPDataShards   int
	KCPParityShards int

	// Throughput / latency tuning
	MSS      int // TCP max segment size (0 = auto)
	SoRcvBuf int // per-socket receive buffer (bytes)
	SoSndBuf int // per-socket send buffer (bytes)

	// ProxyProtocol makes the server prepend a PROXY protocol v2 header to
	// every forwarded connection, so the service behind the tunnel sees the
	// real client IP instead of the tunnel's. Panels need this to enforce
	// per-user device/IP limits. The backend must be configured to expect it.
	ProxyProtocol bool

	// MaxConnections caps simultaneous forwarded connections (0 = unlimited).
	MaxConnections int
	// BandwidthMbps caps total tunnel throughput in Mbit/s (0 = unlimited).
	BandwidthMbps int

	// Sniffer web panel
	Sniffer bool
	WebPort int

	// TLS (server, wss/wssmux only)
	TLSCert string
	TLSKey  string
	// ACMEDomain, when set, makes the tunnel obtain a Let's Encrypt certificate
	// for that domain instead of using the generated self-signed one.
	ACMEDomain string
	ACMEEmail  string

	// Edge/CDN IP override (client, websocket transports only)
	EdgeIP string
}

// writeTuning emits the throughput/latency knobs shared by server and client.
func (s TunnelSpec) writeTuning(p func(string, ...any)) {
	if s.MSS > 0 {
		p("mss = %d\n", s.MSS)
	}
	if s.SoRcvBuf > 0 {
		p("so_rcvbuf = %d\n", s.SoRcvBuf)
	}
	if s.SoSndBuf > 0 {
		p("so_sndbuf = %d\n", s.SoSndBuf)
	}
}

// writeKCP emits the KCP knobs. It is a no-op for every other transport, so a
// tunnel that is not on KCP never carries stale KCP settings in its config.
func (s TunnelSpec) writeKCP(p func(string, ...any)) {
	if !isKCP(s.Transport) {
		return
	}
	p("kcp_mtu = %d\n", s.KCPMTU)
	p("kcp_interval = %d\n", s.KCPInterval)
	p("kcp_resend = %d\n", s.KCPResend)
	p("kcp_nodelay = %d\n", s.KCPNoDelay)
	p("kcp_nocongestion = %d\n", s.KCPNoCongestion)
	p("kcp_sndwnd = %d\n", s.KCPSndWnd)
	p("kcp_rcvwnd = %d\n", s.KCPRcvWnd)
	p("kcp_acknodelay = %t\n", s.KCPAckNoDelay)
	p("kcp_datashards = %d\n", s.KCPDataShards)
	p("kcp_parityshards = %d\n", s.KCPParityShards)
}

// isMux reports whether a transport multiplexes over SMUX.
func isMux(t string) bool {
	return t == "tcpmux" || t == "wsmux" || t == "wssmux" || t == "kcp"
}

// isKCP reports whether a transport rides on KCP (reliable ARQ over UDP).
func isKCP(t string) bool {
	return t == "kcp"
}

// IsDatagram reports whether a transport carries datagrams (UDP/KCP), for
// callers outside the package — a TCP probe against one is meaningless.
func IsDatagram(t string) bool { return isDatagram(t) }

// isDatagram reports whether a transport is carried in UDP datagrams. Such a
// tunnel never shows up in the TCP listen table and cannot be probed with a
// TCP connect, so every check that assumes TCP has to skip it.
func isDatagram(t string) bool {
	return t == "udp" || t == "kcp"
}

// supportsProxyProtocol reports whether a transport can prepend the PROXY
// protocol header. The plain websocket and raw UDP transports cannot: one has
// no place to put it in its framing, the other carries datagrams with no
// connection to describe.
func supportsProxyProtocol(t string) bool {
	switch t {
	case "tcp", "tcpmux", "kcp", "wsmux", "wssmux", "stealth":
		return true
	}
	return false
}

// isWS reports whether a transport rides over websocket.
func isWS(t string) bool {
	return t == "ws" || t == "wss" || t == "wsmux" || t == "wssmux"
}

// needsTLS reports whether a transport terminates TLS on the server and
// therefore requires a certificate/key pair.
func needsTLS(t string) bool {
	return t == "wss" || t == "wssmux"
}

// validTransport reports whether t is one of the engine's supported transports.
func validTransport(t string) bool {
	switch t {
	case "tcp", "tcpmux", "udp", "kcp", "ws", "wss", "wsmux", "wssmux", "stealth":
		return true
	}
	return false
}

// Render returns the TOML representation of the tunnel.
func (s TunnelSpec) Render() string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	b.WriteString("# Generated by backpack — do not edit while the service is running.\n")
	p("# name = \"%s\"\n\n", s.Name)

	if s.Role == "server" {
		b.WriteString("[server]\n")
		p("bind_addr = %q\n", s.BindAddr)
		p("transport = %q\n", s.Transport)
		if s.Preset != "" {
			p("preset = %q\n", s.Preset)
		}
		p("token = %q\n", s.Token)
		p("channel_size = %d\n", s.ChannelSize)
		p("keepalive_period = %d\n", s.KeepAlive)
		p("nodelay = %t\n", s.Nodelay)
		p("heartbeat = %d\n", s.Heartbeat)
		p("log_level = %q\n", s.LogLevel)
		if s.LogFormat != "" {
			p("log_format = %q\n", s.LogFormat)
		}
		s.writeTuning(p)
		s.writeKCP(p)
		if s.Transport == "tcp" {
			// accept_udp is only honoured by the plain TCP transport in the engine.
			p("accept_udp = %t\n", s.AcceptUDP)
		}
		if needsTLS(s.Transport) {
			p("tls_cert = %q\n", s.TLSCert)
			p("tls_key = %q\n", s.TLSKey)
			// Emitted only when in use, so a config written before Let's
			// Encrypt existed stays byte-identical after an edit.
			if s.ACMEDomain != "" {
				p("acme_domain = %q\n", s.ACMEDomain)
				if s.ACMEEmail != "" {
					p("acme_email = %q\n", s.ACMEEmail)
				}
			}
		}
		if isMux(s.Transport) {
			p("mux_con = %d\n", s.MuxCon)
			p("mux_version = %d\n", s.MuxVersion)
			p("mux_framesize = %d\n", s.MuxFrameSize)
			p("mux_recievebuffer = %d\n", s.MuxRecvBuffer)
			p("mux_streambuffer = %d\n", s.MuxStreamBuffer)
		}
		if supportsProxyProtocol(s.Transport) {
			p("proxy_protocol = %t\n", s.ProxyProtocol)
		}
		if s.MaxConnections > 0 {
			p("max_connections = %d\n", s.MaxConnections)
		}
		if s.BandwidthMbps > 0 {
			p("bandwidth_mbps = %d\n", s.BandwidthMbps)
		}
		p("sniffer = %t\n", s.Sniffer)
		if s.WebPort > 0 {
			p("web_port = %d\n", s.WebPort)
		}
		b.WriteString("ports = [\n")
		for _, port := range s.Ports {
			p("    %q,\n", port)
		}
		b.WriteString("]\n")
		return b.String()
	}

	// client
	b.WriteString("[client]\n")
	p("remote_addr = %q\n", s.RemoteAddr)
	if len(s.FallbackAddrs) > 0 {
		b.WriteString("fallback_addrs = [\n")
		for _, a := range s.FallbackAddrs {
			p("    %q,\n", a)
		}
		b.WriteString("]\n")
	}
	p("transport = %q\n", s.Transport)
	if s.Preset != "" {
		p("preset = %q\n", s.Preset)
	}
	p("token = %q\n", s.Token)
	p("connection_pool = %d\n", s.ConnectionPool)
	p("aggressive_pool = %t\n", s.AggressivePool)
	p("keepalive_period = %d\n", s.KeepAlive)
	p("nodelay = %t\n", s.Nodelay)
	if s.LoadBalance {
		p("load_balance = true\n")
	}
	p("retry_interval = %d\n", 3)
	p("dial_timeout = %d\n", 10)
	p("log_level = %q\n", s.LogLevel)
	if s.LogFormat != "" {
		p("log_format = %q\n", s.LogFormat)
	}
	s.writeTuning(p)
	s.writeKCP(p)
	if isWS(s.Transport) && s.EdgeIP != "" {
		p("edge_ip = %q\n", s.EdgeIP)
	}
	if isMux(s.Transport) {
		p("mux_session = %d\n", s.MuxCon)
		p("mux_version = %d\n", s.MuxVersion)
		p("mux_framesize = %d\n", s.MuxFrameSize)
		p("mux_recievebuffer = %d\n", s.MuxRecvBuffer)
		p("mux_streambuffer = %d\n", s.MuxStreamBuffer)
	}
	p("sniffer = %t\n", s.Sniffer)
	if s.WebPort > 0 {
		p("web_port = %d\n", s.WebPort)
	}
	return b.String()
}

// Save writes the config file, the systemd unit, reloads systemd and starts
// the tunnel. It returns the service name on success.
func (s TunnelSpec) Save() (string, error) {
	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(app.ConfigPath(s.Name), []byte(s.Render()), 0644); err != nil {
		return "", err
	}
	if err := writeUnit(s.Name); err != nil {
		return "", err
	}
	if err := DaemonReload(); err != nil {
		return "", err
	}
	service := app.ServiceName(s.Name)
	if err := StartService(service); err != nil {
		return service, err
	}
	return service, nil
}
