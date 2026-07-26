package e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/client"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/server"
)

// kcpFromSpec mirrors the preset's KCP knobs into an engine config.
func kcpFromSpec(s manage.TunnelSpec) config.KCPConfig {
	return config.KCPConfig{
		MTU:          s.KCPMTU,
		Interval:     s.KCPInterval,
		Resend:       s.KCPResend,
		NoDelay:      s.KCPNoDelay,
		NoCongestion: s.KCPNoCongestion,
		SndWnd:       s.KCPSndWnd,
		RcvWnd:       s.KCPRcvWnd,
		AckNoDelay:   s.KCPAckNoDelay,
		DataShards:   s.KCPDataShards,
		ParityShards: s.KCPParityShards,
	}
}

// Presets, end to end.
//
// The preset values are what a newly created tunnel actually runs with, and
// they are large enough now (an 8 MB per-stream window on Aggressive) that a
// mistake would be rejected by smux at session setup rather than merely being
// slow. The rest of the suite builds configs from hardcoded numbers, so
// without this nothing would exercise the values real tunnels get.
func TestEveryPresetCarriesTrafficOnMuxTransports(t *testing.T) {
	// The mux transports are the ones the stream/session windows apply to.
	for _, transport := range []string{"tcpmux", "wsmux", "kcp"} {
		for _, preset := range []string{manage.PresetBalance, manage.PresetTurbo, manage.PresetAggressive} {
			t.Run(transport+"/"+preset, func(t *testing.T) {
				var spec manage.TunnelSpec
				manage.ApplyPreset(&spec, preset)

				backend := startEchoBackend(t)
				tunnelPort := freePort(t)
				entryPort := freePort(t)
				token := "preset-token-0123456789abcdefgh"

				srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token)
				cliCfg := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

				// Overwrite exactly the knobs the preset owns.
				srvCfg.ChannelSize = spec.ChannelSize
				srvCfg.MuxCon = spec.MuxCon
				srvCfg.MuxVersion = spec.MuxVersion
				srvCfg.MaxFrameSize = spec.MuxFrameSize
				srvCfg.MaxReceiveBuffer = spec.MuxRecvBuffer
				srvCfg.MaxStreamBuffer = spec.MuxStreamBuffer
				srvCfg.SO_RCVBUF, srvCfg.SO_SNDBUF = spec.SoRcvBuf, spec.SoSndBuf

				cliCfg.ConnectionPool = spec.ConnectionPool
				cliCfg.MuxSession = spec.MuxCon
				cliCfg.MuxVersion = spec.MuxVersion
				cliCfg.MaxFrameSize = spec.MuxFrameSize
				cliCfg.MaxReceiveBuffer = spec.MuxRecvBuffer
				cliCfg.MaxStreamBuffer = spec.MuxStreamBuffer
				cliCfg.SO_RCVBUF, cliCfg.SO_SNDBUF = spec.SoRcvBuf, spec.SoSndBuf

				// KCP rides on the same preset; both ends must agree on FEC.
				srvCfg.KCPConfig = kcpFromSpec(spec)
				cliCfg.KCPConfig = kcpFromSpec(spec)

				ctx, cancel := context.WithCancel(context.Background())
				var wg sync.WaitGroup
				t.Cleanup(func() { cancel(); wg.Wait() })

				srv := server.NewServer(srvCfg, ctx)
				wg.Add(1)
				go func() { defer wg.Done(); srv.Start() }()
				time.Sleep(300 * time.Millisecond)
				cli := client.NewClient(cliCfg, ctx)
				wg.Add(1)
				go func() { defer wg.Done(); cli.Start() }()

				entry := fmt.Sprintf("127.0.0.1:%d", entryPort)
				payload := []byte("preset-" + preset + "-carries-data")
				deadline := time.Now().Add(tunnelReadyTimeout)
				var lastErr error
				for time.Now().Before(deadline) {
					if lastErr = poolRoundTrip(entry, payload); lastErr == nil {
						return
					}
					time.Sleep(250 * time.Millisecond)
				}
				t.Fatalf("%s on %s never carried traffic: %v", preset, transport, lastErr)
			})
		}
	}
}
