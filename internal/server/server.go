package server

import (
	"context"
	"net/http"
	_ "net/http/pprof"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/server/transport"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/utils/network"

	"github.com/sirupsen/logrus"
)

// acmeCacheDir is where Let's Encrypt certificates and the ACME account key
// are kept. It must survive restarts: re-issuing works, but doing it repeatedly
// runs into Let's Encrypt's rate limits, and then the tunnel has no
// certificate at all until the limit resets.
const acmeCacheDir = "/etc/backpack/acme"

type Server struct {
	config  *config.ServerConfig
	forward bool
	ctx     context.Context
	cancel  context.CancelFunc
	logger  *logrus.Logger
}

func NewServer(cfg *config.ServerConfig, parentCtx context.Context) *Server {
	ctx, cancel := context.WithCancel(parentCtx)
	// One process runs one tunnel, so the socket tuning is process-wide.
	network.SetPinTCPBuffers(cfg.SOPinTCP)
	// Off unless this tunnel asked for it; see handlers/zerocopy.go.
	// Off unless this tunnel asked for it; see handlers/zerocopy.go.
	handlers.SetZeroCopy(cfg.ZeroCopy)
	return &Server{
		config: cfg,
		ctx:    ctx,
		cancel: cancel,
		logger: utils.NewLoggerWithFormat(cfg.LogLevel, cfg.LogFormat),
	}
}

// NewForwardOrigin builds the listening Kharej half of an application-level
// forward tunnel. It uses the same transport listener and authentication as a
// reverse server, but accepted data channels are dialled into local backends
// instead of being paired with public ingress sockets.
func NewForwardOrigin(cfg *config.ServerConfig, parentCtx context.Context) *Server {
	s := NewServer(cfg, parentCtx)
	s.forward = true
	return s
}

func (s *Server) Start() {
	// Profiling endpoint, off unless explicitly enabled in the config.
	//
	// Bound to loopback on purpose: pprof has no authentication, and its heap
	// dump contains whatever is in memory — including the tunnel token, which
	// is all an attacker needs to connect. Reach it over SSH instead:
	//   ssh -L 6060:127.0.0.1:6060 root@server
	if s.config.PPROF {
		go func() {
			s.logger.Info("pprof listening on 127.0.0.1:6060 (loopback only)")
			if err := http.ListenAndServe("127.0.0.1:6060", nil); err != nil {
				s.logger.Errorf("pprof server stopped: %v", err)
			}
		}()
	}

	switch s.config.Transport {
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
			AcceptUDP:      s.config.AcceptUDP,
			MSS:            s.config.MSS,
			SO_RCVBUF:      s.config.SO_RCVBUF,
			SO_SNDBUF:      s.config.SO_SNDBUF,
			ProxyProtocol:  s.config.ProxyProtocol,
			MaxConnections: s.config.MaxConnections,
			BandwidthMbps:  s.config.BandwidthMbps,
			// Stealth is the TCP transport with a Noise record layer over every
			// tunnel connection; everything else about it is identical.
			Stealth: s.config.Transport == config.STEALTH,
			Forward: s.forward,
		}

		tcpServer := transport.NewTCPServer(s.ctx, tcpConfig, s.logger)
		go tcpServer.Start()

	case config.KCP, config.XDI, config.SPOOF:
		// The spoof transport in pipe mode is a bare datagram relay for
		// WireGuard, not a KCP tunnel — handle it separately and stop here.
		if s.config.Transport == config.SPOOF && s.config.SpoofPipe {
			up, down := network.ResolveSpoofDirections(s.config.SpoofProfile, s.config.SpoofUplink, s.config.SpoofDownlink)
			pipeAddr := s.config.SpoofPipeAddr
			if pipeAddr == "" {
				pipeAddr = "127.0.0.1:51820"
			}
			pipeCfg := &transport.SpoofPipeConfig{
				Token: s.config.Token,
				Carrier: network.SpoofCarrier{
					Uplink: up, Downlink: down,
					SrcIP: s.config.SpoofSrcIP, SrcPool: s.config.SpoofSrcPool,
					PeerIP: s.config.SpoofPeerIP, Interface: s.config.SpoofInterface,
				},
				PeerIP:   s.config.SpoofPeerIP,
				PipeAddr: pipeAddr,
				Retry:    time.Duration(s.config.Heartbeat) * time.Second,
			}
			go transport.NewSpoofPipeServer(s.ctx, pipeCfg, s.logger).Start()
			break
		}

		kcp := s.config.KCPConfig.WithDefaults()
		useICMP := s.config.Transport == config.XDI
		useSpoof := s.config.Transport == config.SPOOF
		kcpConfig := &transport.KcpConfig{
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
			UseSpoof:         useSpoof,
			SpoofProfile:     s.config.SpoofProfile,
			SpoofUplink:      s.config.SpoofUplink,
			SpoofDownlink:    s.config.SpoofDownlink,
			SpoofSrcIP:       s.config.SpoofSrcIP,
			SpoofSrcPool:     s.config.SpoofSrcPool,
			SpoofPeerIP:      s.config.SpoofPeerIP,
			SpoofInterface:   s.config.SpoofInterface,
			Forward:          s.forward,
		}

		kcpServer := transport.NewKcpServer(s.ctx, kcpConfig, s.logger)
		go kcpServer.Start()

	case config.QUIC:
		quicConfig := &transport.QuicConfig{
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
			Forward:        s.forward,
		}

		quicServer := transport.NewQuicServer(s.ctx, quicConfig, s.logger)
		go quicServer.Start()

	case config.TCPMUX:
		tcpMuxConfig := &transport.TcpMuxConfig{
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
			Forward:          s.forward,
		}

		tcpMuxServer := transport.NewTcpMuxServer(s.ctx, tcpMuxConfig, s.logger)
		go tcpMuxServer.Start()

	case config.WS, config.WSS:
		wsConfig := &transport.WsConfig{
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
			Mode:         s.config.Transport,
			SimpleAuth:   s.config.SimpleAuth,
			TLSCertFile:  s.config.TLSCertFile,
			ACMEDomain:   s.config.ACMEDomain,
			ACMEEmail:    s.config.ACMEEmail,
			ACMECacheDir: acmeCacheDir,
			TLSKeyFile:   s.config.TLSKeyFile,

			MaxConnections: s.config.MaxConnections,
			BandwidthMbps:  s.config.BandwidthMbps,
			Forward:        s.forward,
		}

		wsServer := transport.NewWSServer(s.ctx, wsConfig, s.logger)
		go wsServer.Start()

	case config.WSMUX, config.WSSMUX:
		wsMuxConfig := &transport.WsMuxConfig{
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
			Mode:             s.config.Transport,
			SimpleAuth:       s.config.SimpleAuth,
			TLSCertFile:      s.config.TLSCertFile,
			ACMEDomain:       s.config.ACMEDomain,
			ACMEEmail:        s.config.ACMEEmail,
			ACMECacheDir:     acmeCacheDir,
			TLSKeyFile:       s.config.TLSKeyFile,
			ProxyProtocol:    s.config.ProxyProtocol,
			MaxConnections:   s.config.MaxConnections,
			BandwidthMbps:    s.config.BandwidthMbps,
			Forward:          s.forward,
		}

		wsMuxServer := transport.NewWSMuxServer(s.ctx, wsMuxConfig, s.logger)
		go wsMuxServer.Start()

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
			Forward:     s.forward,
		}

		udpServer := transport.NewUDPServer(s.ctx, udpConfig, s.logger)
		go udpServer.Start()

	default:
		s.logger.Fatal("invalid transport type: ", s.config.Transport)
	}

	<-s.ctx.Done()

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
