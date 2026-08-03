package cmd

import (
	"github.com/backpack/backpack/config"
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
	checkProxy(cfg)
}

// checkProxy rejects a proxy URL that cannot work, at load time.
//
// A misspelled scheme or a missing port would otherwise surface as a tunnel
// that simply never connects, with the reason buried in a dial error that names
// the proxy rather than the typo. Failing here says which line of the file is
// wrong, before anything has started.
func checkProxy(cfg *config.Config) {
	if cfg.Client.Proxy == "" {
		return
	}
	p, err := network.ParseProxy(cfg.Client.Proxy)
	if err != nil {
		logger.Fatalf("invalid proxy setting: %v", err)
	}

	// The datagram transports cannot use it, and half-using it would be worse
	// than refusing. Their control channel is TCP and would go through the
	// proxy quite happily, while their data is UDP, which an HTTP CONNECT
	// cannot carry at all and which SOCKS5 only carries through a separate
	// UDP ASSOCIATE this client does not speak. The tunnel would come up and
	// carry nothing — the failure that is hardest to attribute to its cause.
	switch cfg.Client.Transport {
	case config.UDP, config.KCP:
		logger.Fatalf("proxy is not supported on the %s transport: its data is carried in UDP datagrams, which a TCP proxy cannot relay. Use tcp, tcpmux, ws, wss or wsmux, or remove the proxy setting.", cfg.Client.Transport)
	}

	logger.Infof("the tunnel server will be reached through %s", p)
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
