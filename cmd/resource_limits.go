package cmd

import (
	"fmt"

	"github.com/backpack/backpack/config"
)

const (
	maxChannelSize        = 8 * 1024
	maxConnectionPool     = 64
	maxMuxSessions        = 64
	maxMuxConcurrency     = 256
	maxMuxFrameSize       = 65_535
	maxMuxReceiveBuffer   = 32 << 20
	maxSocketBuffer       = 8 << 20
	muxClientMemoryBudget = 64 << 20
)

// enforceResourceLimits bounds values that directly size channels, socket
// buffers, goroutine bursts, or SMUX receive windows. It returns warnings so
// callers can choose how to surface operator-visible corrections.
func enforceResourceLimits(cfg *config.Config) []string {
	var warnings []string
	capValue := func(name string, value *int, maximum int) {
		if *value > maximum {
			warnings = append(warnings, fmt.Sprintf("%s=%d exceeds the safe limit; using %d", name, *value, maximum))
			*value = maximum
		}
	}

	capValue("server.channel_size", &cfg.Server.ChannelSize, maxChannelSize)
	capValue("client.connection_pool", &cfg.Client.ConnectionPool, maxConnectionPool)
	capValue("server.mux_session", &cfg.Server.MuxSession, maxMuxSessions)
	capValue("client.mux_session", &cfg.Client.MuxSession, maxMuxSessions)
	capValue("server.mux_con", &cfg.Server.MuxCon, maxMuxConcurrency)
	capValue("server.mux_framesize", &cfg.Server.MaxFrameSize, maxMuxFrameSize)
	capValue("client.mux_framesize", &cfg.Client.MaxFrameSize, maxMuxFrameSize)
	capValue("server.mux_recievebuffer", &cfg.Server.MaxReceiveBuffer, maxMuxReceiveBuffer)
	capValue("client.mux_recievebuffer", &cfg.Client.MaxReceiveBuffer, maxMuxReceiveBuffer)
	capValue("server.so_rcvbuf", &cfg.Server.SO_RCVBUF, maxSocketBuffer)
	capValue("server.so_sndbuf", &cfg.Server.SO_SNDBUF, maxSocketBuffer)
	capValue("client.so_rcvbuf", &cfg.Client.SO_RCVBUF, maxSocketBuffer)
	capValue("client.so_sndbuf", &cfg.Client.SO_SNDBUF, maxSocketBuffer)

	if cfg.Server.MaxStreamBuffer > cfg.Server.MaxReceiveBuffer {
		warnings = append(warnings, "server.mux_streambuffer exceeds mux_recievebuffer; using the receive-buffer limit")
		cfg.Server.MaxStreamBuffer = cfg.Server.MaxReceiveBuffer
	}
	if cfg.Client.MaxStreamBuffer > cfg.Client.MaxReceiveBuffer {
		warnings = append(warnings, "client.mux_streambuffer exceeds mux_recievebuffer; using the receive-buffer limit")
		cfg.Client.MaxStreamBuffer = cfg.Client.MaxReceiveBuffer
	}

	if clientUsesSMUX(cfg.Client.Transport) && cfg.Client.MaxReceiveBuffer > 0 {
		maxPoolForBudget := muxClientMemoryBudget / cfg.Client.MaxReceiveBuffer
		if maxPoolForBudget < 1 {
			maxPoolForBudget = 1
		}
		if cfg.Client.ConnectionPool > maxPoolForBudget {
			warnings = append(warnings, fmt.Sprintf(
				"client connection_pool × mux_recievebuffer exceeds %d MiB; reducing connection_pool from %d to %d",
				muxClientMemoryBudget>>20, cfg.Client.ConnectionPool, maxPoolForBudget))
			cfg.Client.ConnectionPool = maxPoolForBudget
		}
	}
	return warnings
}

func clientUsesSMUX(transport config.TransportType) bool {
	switch transport {
	case config.TCPMUX, config.KCP, config.XDI, config.WSMUX, config.WSSMUX, config.SPOOF:
		return true
	default:
		return false
	}
}
