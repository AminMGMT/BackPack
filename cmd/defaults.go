package cmd

import (
	"net"
	"os"
	"os/exec"
	"runtime"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/utils/network"

	"github.com/sirupsen/logrus"
)

const ( // Default values
	defaultToken          = "backpack"
	defaultChannelSize    = 2048
	defaultRetryInterval  = 3 // only for client
	defaultConnectionPool = 8
	defaultLogLevel       = "info"
	defaultMuxSession     = 1
	defaultKeepAlive      = 75
	deafultHeartbeat      = 40 // 40 seconds
	defaultDialTimeout    = 10 // 10 seconds
	// related to smux
	// defaultMuxVersion stays at 1 because smux has no version negotiation at
	// all: a session whose two ends disagree is torn down on the first frame
	// with "invalid protocol". Raising the default would therefore break every
	// mux tunnel the moment one side was upgraded and the other was not — the
	// control channel would come up and no data would pass, which is precisely
	// the failure that is hardest to diagnose. Version 2 is worth having (it is
	// what makes mux_streambuffer mean anything, see below), but it has to be
	// negotiated first, not defaulted into.
	defaultMuxVersion       = 1
	defaultMaxFrameSize     = 32768   // 32KB
	defaultMaxReceiveBuffer = 4194304 // 4MB
	defaultMaxStreamBuffer  = 65536   // 64KB
	defaultSnifferLog       = "backpack.json"
	defaultMuxCon           = 8
)

func applyDefaults(cfg *config.Config) {
	if cfg.EffectiveEngine() != config.EngineReverse && cfg.EffectiveEngine() != config.EngineForward {
		return
	}
	// Token
	if cfg.Server.Token == "" {
		cfg.Server.Token = defaultToken
	}
	if cfg.Client.Token == "" {
		cfg.Client.Token = defaultToken
	}

	// Nodelay default is false if not valid value found

	// Channel size
	if cfg.Server.ChannelSize <= 0 {
		cfg.Server.ChannelSize = defaultChannelSize
	}

	// Loglevel
	if _, err := logrus.ParseLevel(cfg.Client.LogLevel); err != nil {
		cfg.Client.LogLevel = defaultLogLevel
	}

	if _, err := logrus.ParseLevel(cfg.Server.LogLevel); err != nil {
		cfg.Server.LogLevel = defaultLogLevel
	}

	// Retry interval
	if cfg.Client.RetryInterval <= 0 {
		cfg.Client.RetryInterval = defaultRetryInterval
	}

	// Connection pool
	if cfg.Client.ConnectionPool <= 0 {
		cfg.Client.ConnectionPool = defaultConnectionPool
	}

	// Mux Session
	if cfg.Server.MuxSession <= 0 {
		cfg.Server.MuxSession = defaultMuxSession
	}
	if cfg.Client.MuxSession <= 0 {
		cfg.Client.MuxSession = defaultMuxSession
	}

	// PPROF default is false if not valid value found

	// keep alive
	if cfg.Server.Keepalive <= 0 {
		cfg.Server.Keepalive = defaultKeepAlive
	}
	if cfg.Client.Keepalive <= 0 {
		cfg.Client.Keepalive = defaultKeepAlive
	}

	// Mux version. Left alone when it is 1 or 2 — an explicit choice is still
	// honoured — and otherwise reset to auto, which has the server settle it on
	// the control channel. Anything out of range is a typo, and auto is the
	// safe reading of one.
	if cfg.Server.MuxVersion != 1 && cfg.Server.MuxVersion != 2 {
		cfg.Server.MuxVersion = network.MuxVersionAuto
	}
	if cfg.Client.MuxVersion != 1 && cfg.Client.MuxVersion != 2 {
		cfg.Client.MuxVersion = network.MuxVersionAuto
	}
	// MaxFrameSize
	if cfg.Server.MaxFrameSize <= 0 {
		cfg.Server.MaxFrameSize = defaultMaxFrameSize
	}
	if cfg.Client.MaxFrameSize <= 0 {
		cfg.Client.MaxFrameSize = defaultMaxFrameSize
	}
	// MaxReceiveBuffer
	if cfg.Server.MaxReceiveBuffer <= 0 {
		cfg.Server.MaxReceiveBuffer = defaultMaxReceiveBuffer
	}
	if cfg.Client.MaxReceiveBuffer <= 0 {
		cfg.Client.MaxReceiveBuffer = defaultMaxReceiveBuffer
	}
	// MaxStreamBuffer
	if cfg.Server.MaxStreamBuffer <= 0 {
		cfg.Server.MaxStreamBuffer = defaultMaxStreamBuffer
	}
	if cfg.Client.MaxStreamBuffer <= 0 {
		cfg.Client.MaxStreamBuffer = defaultMaxStreamBuffer
	}
	// WebPort returns 0 if not exists

	// SnifferLog
	if cfg.Server.SnifferLog == "" {
		cfg.Server.SnifferLog = defaultSnifferLog
	}
	if cfg.Client.SnifferLog == "" {
		cfg.Client.SnifferLog = defaultSnifferLog
	}
	// Heartbeat
	if cfg.Server.Heartbeat < 1 { // Minimum accepted interval is 1 second
		cfg.Server.Heartbeat = deafultHeartbeat
	}

	// Timeout
	if cfg.Client.DialTimeout < 1 { // Minimum accepted value is 1 second
		cfg.Client.DialTimeout = defaultDialTimeout
	}

	// Mux concurrancy
	if cfg.Server.MuxCon < 1 {
		cfg.Server.MuxCon = defaultMuxCon
	}

	warnUnusedStreamBuffer(cfg)
	checkOutbound(cfg)
	checkXdi(cfg)
	checkSpoof(cfg)
}

// checkSpoof refuses a spoof tunnel that cannot possibly work, before it tries,
// and validates the profile so an unknown one is named here rather than silently
// defaulting deep in the transport.
//
// Like xdi it needs a raw socket (root or CAP_NET_RAW) and is Linux only. The
// warning is deliberately blunt: source spoofing only carries traffic where the
// upstream network does not drop forged-source packets, and the tcp profile
// additionally needs a firewall rule so the host kernel does not RST the flow.
func checkSpoof(cfg *config.Config) {
	usesSpoof := cfg.Server.Transport == config.SPOOF || cfg.Client.Transport == config.SPOOF
	if !usesSpoof {
		return
	}
	if runtime.GOOS != "linux" {
		logger.Fatalf("the spoof transport is only available on Linux (it needs a raw IP socket)")
	}
	if os.Geteuid() != 0 {
		logger.Fatalf("the spoof transport needs a raw IP socket, which requires root or CAP_NET_RAW — run as root, or grant the capability with: setcap cap_net_raw+ep %s", app.BinPath)
	}
	// Validate both directions' profiles of whichever side is on spoof here. A
	// direction defaults to spoof_profile, then to udp.
	sc := cfg.Server.SpoofConfig
	if cfg.Client.Transport == config.SPOOF {
		sc = cfg.Client.SpoofConfig
	}
	for _, p := range []string{sc.SpoofProfile, sc.SpoofUplink, sc.SpoofDownlink} {
		if _, err := network.ParseSpoofProfile(p); err != nil {
			logger.Fatalf("invalid spoof profile: %v", err)
		}
	}
	up, down := network.ResolveSpoofDirections(sc.SpoofProfile, sc.SpoofUplink, sc.SpoofDownlink)
	if up != down {
		logger.Infof("spoof is asymmetric: uplink (client→server) %s, downlink (server→client) %s. Both ends must set the same pair.", up, down)
	}
	// The kernel RSTs a forged TCP segment on the side that RECEIVES tcp, so the
	// iptables rule matters whenever either direction is tcp.
	if up == "tcp" || down == "tcp" {
		if _, err := exec.LookPath("iptables"); err != nil {
			logger.Warn("a tcp spoof direction needs the iptables binary to suppress the kernel's RSTs, and it was not found. The tunnel will run, but those RSTs may disrupt it. Install iptables, or use udp/icmp.")
		} else {
			logger.Info("tcp spoof direction: a targeted iptables rule dropping the kernel's RSTs on the tunnel port is installed on start and removed on stop.")
		}
	}

	// The server cannot learn the client's real address from the forged packets,
	// so it must be told it. The client derives the server's real address from
	// remote_addr, so spoof_peer_ip is optional there.
	if cfg.Server.Transport == config.SPOOF && net.ParseIP(cfg.Server.SpoofPeerIP).To4() == nil {
		logger.Fatalf("the spoof transport needs spoof_peer_ip set to the client's real IPv4 address on the server (it cannot be learned from the forged packets)")
	}

	// A typo in any forged source is caught here rather than silently sending
	// nothing.
	pool := cfg.Server.SpoofSrcPool
	if cfg.Client.Transport == config.SPOOF {
		pool = cfg.Client.SpoofSrcPool
	}
	for _, ip := range pool {
		if net.ParseIP(ip).To4() == nil {
			logger.Fatalf("invalid IPv4 %q in spoof_src_pool", ip)
		}
	}

	logger.Warn("spoof is experimental: it forges the source address of raw IP packets. It only carries traffic where the upstream network does not drop forged-source packets (no egress/BCP38 filtering) — prove this with the spoof tester on your real route before relying on it.")
}

// checkXdi refuses an xdi tunnel that cannot possibly work, before it tries.
//
// The transport needs a raw ICMP socket, which needs root or CAP_NET_RAW.
// Without it the socket open fails deep inside the transport with an errno an
// operator should not have to decode; caught here, it names the cause and what
// to do. The server is normally root anyway — this is for the case where it is
// not.
func checkXdi(cfg *config.Config) {
	usesXdi := cfg.Server.Transport == config.XDI || cfg.Client.Transport == config.XDI
	if !usesXdi {
		return
	}
	if runtime.GOOS != "linux" {
		logger.Fatalf("the xdi transport is only available on Linux (it needs a raw ICMP socket)")
	}
	if os.Geteuid() != 0 {
		logger.Fatalf("the xdi transport needs a raw ICMP socket, which requires root or CAP_NET_RAW — run as root, or grant the capability with: setcap cap_net_raw+ep %s", app.BinPath)
	}
	logger.Warn("xdi is experimental: it carries the tunnel inside ICMP echo (ping) packets, for networks that filter UDP and TCP but not ICMP. It is slower than the other transports and heavier on ICMP rate limits.")
}

// checkOutbound rejects a proxy or a routing binding that cannot work, at load
// time.
//
// A misspelled scheme, a missing port or an interface that does not exist would
// otherwise surface as a tunnel that simply never connects, with the reason
// buried in a dial error. Failing here says which line of the file is wrong,
// before anything has started.
func checkOutbound(cfg *config.Config) {
	proxy, err := network.ParseProxy(cfg.Client.Proxy)
	if err != nil {
		logger.Fatalf("invalid proxy setting: %v", err)
	}
	out := &network.Outbound{
		Proxy:     proxy,
		LocalAddr: cfg.Client.LocalAddr,
		Interface: cfg.Client.Interface,
		Mark:      cfg.Client.SOMark,
	}
	if !out.IsSet() {
		return
	}
	if err := out.Validate(); err != nil {
		logger.Fatalf("invalid outbound setting: %v", err)
	}

	// The datagram transports carry their data outside the TCP dialer entirely,
	// so none of this would reach it. Half-applying would be worse than
	// refusing: on udp the control channel is TCP and would honour every one of
	// these settings, while the data went out by whatever route the kernel
	// chose. The tunnel would come up and quietly leave by the wrong link — or,
	// with a proxy, come up and carry nothing at all. On kcp, xdi and quic not
	// even the control channel is TCP, so the settings would be accepted and
	// then ignored outright.
	switch cfg.Client.Transport {
	case config.UDP, config.KCP, config.XDI, config.QUIC, config.SPOOF:
		logger.Fatalf("proxy, local_addr, interface and so_mark are not supported on the %s transport: its data is not carried over the TCP dialer these settings apply to. Use tcp, tcpmux, ws, wss or wsmux, or remove them.", cfg.Client.Transport)
	}

	logger.Infof("the tunnel server will be reached %s", out)
}

// warnUnusedStreamBuffer says out loud that mux_streambuffer does nothing on
// mux version 1.
//
// smux only applies a per-stream receive window in version 2 — in version 1
// there is no per-stream flow control at all, only the session-wide
// mux_receivebuffer. So an operator who pins mux_version to 1 and then sets
// mux_streambuffer to tune a slow tunnel changes nothing whatsoever, and has no
// way to find that out: the setting is accepted, reported back by the panel,
// and silently ignored by the library. Tuning that appears to work and does not
// is worse than tuning that is refused.
//
// Only a pinned 1 is worth warning about. On auto the version is settled with
// the server, and the answer is 2 whenever both ends are new enough to be
// asked — so the setting will be applied unless the peer is too old, which is
// reported on the control channel instead.
const unusedStreamBufferWarning = "mux_streambuffer has no effect while mux_version is pinned to 1: smux only applies a per-stream window on version 2. Remove mux_version to let the two ends agree on it, or set it to 2 on both."

func warnUnusedStreamBuffer(cfg *config.Config) {
	if cfg.Server.MaxStreamBuffer > 0 && cfg.Server.MuxVersion == 1 && cfg.Server.BindAddr != "" {
		logger.Warn(unusedStreamBufferWarning)
	}
	if cfg.Client.MaxStreamBuffer > 0 && cfg.Client.MuxVersion == 1 && cfg.Client.RemoteAddr != "" {
		logger.Warn(unusedStreamBufferWarning)
	}
}
