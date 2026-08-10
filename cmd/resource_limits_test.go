package cmd

import (
	"testing"

	"github.com/backpack/backpack/config"
)

func TestResourceLimitsBoundSMUXMemoryAndInvalidWindows(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			ChannelSize:      1 << 20,
			MuxSession:       1000,
			MuxCon:           1000,
			MaxFrameSize:     1 << 20,
			MaxReceiveBuffer: 1 << 30,
			MaxStreamBuffer:  1 << 30,
			SO_RCVBUF:        1 << 30,
			SO_SNDBUF:        1 << 30,
		},
		Client: config.ClientConfig{
			Transport:        config.WSMUX,
			ConnectionPool:   1000,
			MuxSession:       1000,
			MaxFrameSize:     1 << 20,
			MaxReceiveBuffer: 1 << 30,
			MaxStreamBuffer:  1 << 30,
			SO_RCVBUF:        1 << 30,
			SO_SNDBUF:        1 << 30,
		},
	}
	warnings := enforceResourceLimits(cfg)
	if len(warnings) == 0 {
		t.Fatal("dangerous configuration was silently accepted")
	}
	if cfg.Server.ChannelSize != maxChannelSize || cfg.Server.MuxSession != maxMuxSessions || cfg.Server.MuxCon != maxMuxConcurrency {
		t.Fatalf("server allocation limits not applied: %+v", cfg.Server)
	}
	if cfg.Client.MaxReceiveBuffer != maxMuxReceiveBuffer || cfg.Client.MaxStreamBuffer != maxMuxReceiveBuffer {
		t.Fatalf("client SMUX windows not bounded: receive=%d stream=%d", cfg.Client.MaxReceiveBuffer, cfg.Client.MaxStreamBuffer)
	}
	if cfg.Client.ConnectionPool*cfg.Client.MaxReceiveBuffer > muxClientMemoryBudget {
		t.Fatalf("SMUX pool budget exceeded: pool=%d receive=%d", cfg.Client.ConnectionPool, cfg.Client.MaxReceiveBuffer)
	}
	if cfg.Server.SO_RCVBUF != maxSocketBuffer || cfg.Client.SO_SNDBUF != maxSocketBuffer {
		t.Fatal("socket buffers were not bounded")
	}
}

func TestResourceLimitsLeaveSafeValuesUntouched(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{ChannelSize: 1024, MuxSession: 2, MuxCon: 8, MaxFrameSize: 32768, MaxReceiveBuffer: 4 << 20, MaxStreamBuffer: 2 << 20},
		Client: config.ClientConfig{Transport: config.WSMUX, ConnectionPool: 4, MuxSession: 2, MaxFrameSize: 32768, MaxReceiveBuffer: 4 << 20, MaxStreamBuffer: 2 << 20},
	}
	if warnings := enforceResourceLimits(cfg); len(warnings) != 0 {
		t.Fatalf("safe configuration produced warnings: %v", warnings)
	}
	if cfg.Client.ConnectionPool != 4 || cfg.Client.MaxReceiveBuffer != 4<<20 || cfg.Server.ChannelSize != 1024 {
		t.Fatalf("safe configuration changed: %+v", cfg)
	}
}

func TestNonMuxPoolIsNotChargedAgainstSMUXBudget(t *testing.T) {
	cfg := &config.Config{Client: config.ClientConfig{
		Transport: config.TCP, ConnectionPool: 32, MaxReceiveBuffer: 32 << 20, MaxStreamBuffer: 1 << 20,
	}}
	enforceResourceLimits(cfg)
	if cfg.Client.ConnectionPool != 32 {
		t.Fatalf("plain TCP pool was capped as SMUX: %d", cfg.Client.ConnectionPool)
	}
}
