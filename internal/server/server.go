package server

import (
	"context"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/debugserver"
	"github.com/backpack/backpack/internal/server/transport"
	"github.com/backpack/backpack/internal/tunnel/chain"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/backpack/backpack/internal/web"

	"github.com/sirupsen/logrus"
)

// acmeCacheDir is where Let's Encrypt certificates and the ACME account key
// are kept. It must survive restarts: re-issuing works, but doing it repeatedly
// runs into Let's Encrypt's rate limits, and then the tunnel has no
// certificate at all until the limit resets.
const acmeCacheDir = "/etc/backpack/acme"

type Server struct {
	config *config.ServerConfig
	ctx    context.Context
	cancel context.CancelFunc
	logger *logrus.Logger
}

func NewServer(cfg *config.ServerConfig, parentCtx context.Context) *Server {
	ctx, cancel := context.WithCancel(parentCtx)
	// One process runs one tunnel, so the socket tuning is process-wide.
	network.SetPinTCPBuffers(cfg.SOPinTCP)
	// Off unless this tunnel asked for it; see handlers/zerocopy.go.
	handlers.SetZeroCopy(cfg.ZeroCopy)
	// Loopback unless this tunnel asked otherwise; see web/monitorhttp.go.
	web.SetMonitorBind(cfg.WebBind)
	return &Server{
		config: cfg,
		ctx:    ctx,
		cancel: cancel,
		logger: utils.NewLoggerWithFormat(cfg.LogLevel, cfg.LogFormat),
	}
}

func (s *Server) Start() {
	// Profiling endpoint, off unless explicitly enabled in the config.
	//
	// Bound to loopback on purpose: pprof has no authentication, and its heap
	// dump contains whatever is in memory — including the tunnel token, which
	// is all an attacker needs to connect. Reach it over SSH instead:
	//   ssh -L 6060:127.0.0.1:6060 root@server
	//
	// It is tied to this generation's context, and Start does not return until
	// it has let go of the port. A reload starts the next generation as soon as
	// this one is done, so a profiling listener that outlived its generation
	// would be holding a port the next one is about to ask for.
	if s.config.PPROF {
		pprofStopped := make(chan struct{})
		go func() {
			defer close(pprofStopped)
			s.logger.Info("pprof listening on 127.0.0.1:6060 (loopback only)")
			if err := debugserver.Serve(s.ctx, "127.0.0.1:6060"); err != nil {
				s.logger.Errorf("pprof server stopped: %v", err)
			}
		}()
		defer func() { <-pprofStopped }()
	}

	// One transport, or an ordered chain of them. With no fallbacks configured
	// the chain holds a single candidate and behaves exactly as the switch it
	// replaced. With fallbacks it holds each candidate for the dwell and then
	// tries the next, which is how a client whose carrier was filtered finds an
	// ear on this end. See internal/tunnel/chain for why the two ends meet.
	//
	// Candidates run one at a time on purpose: starting a transport binds the
	// forwarded ports, so two live candidates would fight over them.
	ch := chain.New(string(s.config.Transport),
		config.FallbackNames(s.config.FallbackTransports),
		config.Dwell(s.config.FallbackDwell)).
		OnLog(func(m string) { s.logger.Info(m) })
	if !ch.Single() {
		s.logger.Infof("transport fallback chain: %v", ch.Candidates())
	}
	// The server holds each candidate for the whole dwell; the client sweeps.
	//
	// live is the transport currently running, so Start can wait for it below.
	// The chain cancels a candidate's context when it rotates or stops; what it
	// cannot know is when the listeners behind it have actually closed.
	var live runner
	ch.Run(s.ctx, false, func(ctx context.Context, name string) chain.Attempt {
		r := s.startTransport(ctx, config.TransportType(name))
		if r == nil {
			return chain.Attempt{}
		}
		live = r
		return chain.Attempt{Settled: r.Running}
	})

	// Start does not return until the ports are free.
	//
	// Without this it returned as soon as the chain stopped supervising, while
	// the listeners were still closing — and a reload builds the next
	// generation immediately, so the two fought for the same ports. Restart's
	// two-second sleep was the workaround; this is the answer.
	if live != nil {
		// Not s.ctx: that is already cancelled by the time this runs, and
		// waiting on a dead context would wait for nothing.
		wait, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		live.Wait(wait)
		cancel()
	}

	s.logger.Info("all workers stopped successfully")

	// suppress other logs
	s.logger.SetLevel(logrus.FatalLevel)
}

// Stop shuts down the server gracefully
func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
}

// runner is what a started transport gives the chain back.
//
// Running answers whether a client has paired with it, which is what a fallback
// chain polls. Wait blocks until it has let go of its listeners, which is what
// makes "Start returned" mean "the ports are free" — see
// internal/server/transport/listeners.go for why that is not the same thing as
// "the supervisor stopped".
type runner interface {
	Running() bool
	Wait(context.Context)
}

// startTransport launches one transport under ctx and returns it. Cancelling
// ctx tears it down, including the forwarded-port listeners, which is what lets
// a chain run several of these one after another without them fighting over
// the same ports.
func (s *Server) startTransport(ctx context.Context, tr config.TransportType) runner {
	switch tr {
	case config.TCP, config.STEALTH:
		tcpConfig := &transport.TcpConfig{
			BindAddr:       s.config.BindAddr,
			Nodelay:        s.config.Nodelay,
			KeepAlive:      time.Duration(s.config.Keepalive) * time.Second,
			Heartbeat:      time.Duration(s.config.Heartbeat) * time.Second,
			Token:          s.config.Token,
			ChannelSize:    s.config.ChannelSize,
			Ports:          s.config.Ports,
			Sniffer:        s.config.Sniffer,
			WebPort:        s.config.WebPort,
			SnifferLog:     s.config.SnifferLog,
			AcceptUDP:      s.config.ForwardsUDP(),
			MSS:            s.config.MSS,
			SO_RCVBUF:      s.config.SO_RCVBUF,
			SO_SNDBUF:      s.config.SO_SNDBUF,
			ProxyProtocol:  s.config.ProxyProtocol,
			MaxConnections: s.config.MaxConnections,
			BandwidthMbps:  s.config.BandwidthMbps,
			// Stealth is the TCP transport with a Noise record layer over every
			// tunnel connection; everything else about it is identical.
			Stealth: tr == config.STEALTH,
		}

		tcpServer := transport.NewTCPServer(ctx, tcpConfig, s.logger)
		go tcpServer.Start()
		return tcpServer

	case config.KCP, config.XDI, config.PCK:
		kcp := s.config.KCPConfig.WithDefaults()
		useICMP := tr == config.XDI
		kcpConfig := &transport.KcpConfig{
			AcceptUDP:        s.config.ForwardsUDP(),
			BindAddr:         s.config.BindAddr,
			Heartbeat:        time.Duration(s.config.Heartbeat) * time.Second,
			Token:            s.config.Token,
			ChannelSize:      s.config.ChannelSize,
			Ports:            s.config.Ports,
			MuxCon:           s.config.MuxCon,
			MuxVersion:       s.config.MuxVersion,
			MaxFrameSize:     s.config.MaxFrameSize,
			MaxReceiveBuffer: s.config.MaxReceiveBuffer,
			MaxStreamBuffer:  s.config.MaxStreamBuffer,
			Sniffer:          s.config.Sniffer,
			WebPort:          s.config.WebPort,
			SnifferLog:       s.config.SnifferLog,
			SO_RCVBUF:        s.config.SO_RCVBUF,
			SO_SNDBUF:        s.config.SO_SNDBUF,
			ProxyProtocol:    s.config.ProxyProtocol,
			MaxConnections:   s.config.MaxConnections,
			BandwidthMbps:    s.config.BandwidthMbps,
			MTU:              kcp.MTU,
			Interval:         kcp.Interval,
			Resend:           kcp.Resend,
			NoDelay:          kcp.NoDelay,
			NoCongestion:     kcp.NoCongestion,
			SndWnd:           kcp.SndWnd,
			RcvWnd:           kcp.RcvWnd,
			AckNoDelay:       kcp.AckNoDelay,
			DataShards:       kcp.DataShards,
			ParityShards:     kcp.ParityShards,
			UseICMP:          useICMP,
			UsePck:           tr == config.PCK,
			PckInterface:     s.config.PckInterface,
			PckGatewayMAC:    s.config.PckGatewayMAC,
			PckFlags:         s.config.PckFlags,
		}

		kcpServer := transport.NewKcpServer(ctx, kcpConfig, s.logger)
		go kcpServer.Start()
		return kcpServer

	case config.QUIC:
		quicConfig := &transport.QuicConfig{
			AcceptUDP:      s.config.ForwardsUDP(),
			BindAddr:       s.config.BindAddr,
			Heartbeat:      time.Duration(s.config.Heartbeat) * time.Second,
			KeepAlive:      time.Duration(s.config.Keepalive) * time.Second,
			Token:          s.config.Token,
			ChannelSize:    s.config.ChannelSize,
			Ports:          s.config.Ports,
			Sniffer:        s.config.Sniffer,
			WebPort:        s.config.WebPort,
			SnifferLog:     s.config.SnifferLog,
			SO_RCVBUF:      s.config.SO_RCVBUF,
			SO_SNDBUF:      s.config.SO_SNDBUF,
			ProxyProtocol:  s.config.ProxyProtocol,
			MaxConnections: s.config.MaxConnections,
			BandwidthMbps:  s.config.BandwidthMbps,
		}

		quicServer := transport.NewQuicServer(ctx, quicConfig, s.logger)
		go quicServer.Start()
		return quicServer

	case config.TCPMUX:
		tcpMuxConfig := &transport.TcpMuxConfig{
			AcceptUDP:        s.config.ForwardsUDP(),
			BindAddr:         s.config.BindAddr,
			Nodelay:          s.config.Nodelay,
			KeepAlive:        time.Duration(s.config.Keepalive) * time.Second,
			Heartbeat:        time.Duration(s.config.Heartbeat) * time.Second,
			Token:            s.config.Token,
			ChannelSize:      s.config.ChannelSize,
			Ports:            s.config.Ports,
			MuxCon:           s.config.MuxCon,
			MuxVersion:       s.config.MuxVersion,
			MaxFrameSize:     s.config.MaxFrameSize,
			MaxReceiveBuffer: s.config.MaxReceiveBuffer,
			MaxStreamBuffer:  s.config.MaxStreamBuffer,
			Sniffer:          s.config.Sniffer,
			WebPort:          s.config.WebPort,
			SnifferLog:       s.config.SnifferLog,
			MSS:              s.config.MSS,
			SO_RCVBUF:        s.config.SO_RCVBUF,
			SO_SNDBUF:        s.config.SO_SNDBUF,
			ProxyProtocol:    s.config.ProxyProtocol,
			MaxConnections:   s.config.MaxConnections,
			BandwidthMbps:    s.config.BandwidthMbps,
		}

		tcpMuxServer := transport.NewTcpMuxServer(ctx, tcpMuxConfig, s.logger)
		go tcpMuxServer.Start()
		return tcpMuxServer

	case config.WS, config.WSS:
		wsConfig := &transport.WsConfig{
			AcceptUDP:    s.config.ForwardsUDP(),
			BindAddr:     s.config.BindAddr,
			Nodelay:      s.config.Nodelay,
			KeepAlive:    time.Duration(s.config.Keepalive) * time.Second,
			Heartbeat:    time.Duration(s.config.Heartbeat) * time.Second,
			Token:        s.config.Token,
			ChannelSize:  s.config.ChannelSize,
			Ports:        s.config.Ports,
			Sniffer:      s.config.Sniffer,
			WebPort:      s.config.WebPort,
			SnifferLog:   s.config.SnifferLog,
			Mode:         tr,
			SimpleAuth:   s.config.SimpleAuth,
			TLSCertFile:  s.config.TLSCertFile,
			ACMEDomain:   s.config.ACMEDomain,
			ACMEEmail:    s.config.ACMEEmail,
			ACMECacheDir: acmeCacheDir,
			TLSKeyFile:   s.config.TLSKeyFile,
			MSS:          s.config.MSS,

			MaxConnections: s.config.MaxConnections,
			BandwidthMbps:  s.config.BandwidthMbps,
		}

		wsServer := transport.NewWSServer(ctx, wsConfig, s.logger)
		go wsServer.Start()
		return wsServer

	case config.WSMUX, config.WSSMUX:
		wsMuxConfig := &transport.WsMuxConfig{
			AcceptUDP:        s.config.ForwardsUDP(),
			BindAddr:         s.config.BindAddr,
			Nodelay:          s.config.Nodelay,
			KeepAlive:        time.Duration(s.config.Keepalive) * time.Second,
			Heartbeat:        time.Duration(s.config.Heartbeat) * time.Second,
			Token:            s.config.Token,
			ChannelSize:      s.config.ChannelSize,
			Ports:            s.config.Ports,
			MuxCon:           s.config.MuxCon,
			MuxVersion:       s.config.MuxVersion,
			MaxFrameSize:     s.config.MaxFrameSize,
			MaxReceiveBuffer: s.config.MaxReceiveBuffer,
			MaxStreamBuffer:  s.config.MaxStreamBuffer,
			Sniffer:          s.config.Sniffer,
			WebPort:          s.config.WebPort,
			SnifferLog:       s.config.SnifferLog,
			Mode:             tr,
			SimpleAuth:       s.config.SimpleAuth,
			TLSCertFile:      s.config.TLSCertFile,
			ACMEDomain:       s.config.ACMEDomain,
			ACMEEmail:        s.config.ACMEEmail,
			ACMECacheDir:     acmeCacheDir,
			TLSKeyFile:       s.config.TLSKeyFile,
			MSS:              s.config.MSS,
			ProxyProtocol:    s.config.ProxyProtocol,
			MaxConnections:   s.config.MaxConnections,
			BandwidthMbps:    s.config.BandwidthMbps,
		}

		wsMuxServer := transport.NewWSMuxServer(ctx, wsMuxConfig, s.logger)
		go wsMuxServer.Start()
		return wsMuxServer

	case config.UDP:
		udpConfig := &transport.UdpConfig{
			BindAddr:    s.config.BindAddr,
			Heartbeat:   time.Duration(s.config.Heartbeat) * time.Second,
			Token:       s.config.Token,
			ChannelSize: s.config.ChannelSize,
			Ports:       s.config.Ports,
			Sniffer:     s.config.Sniffer,
			WebPort:     s.config.WebPort,
			SnifferLog:  s.config.SnifferLog,
			SO_RCVBUF:   s.config.SO_RCVBUF,
			SO_SNDBUF:   s.config.SO_SNDBUF,
			// Passed like every other transport's. Leaving them off here is
			// what made a udp tunnel accept a limit, save it, render it into
			// the TOML, show it in the panel and never apply it.
			MaxConnections: s.config.MaxConnections,
			BandwidthMbps:  s.config.BandwidthMbps,
		}

		udpServer := transport.NewUDPServer(ctx, udpConfig, s.logger)
		go udpServer.Start()
		return udpServer

	default:
		s.logger.Fatal("invalid transport type: ", tr)
		return nil
	}
}
