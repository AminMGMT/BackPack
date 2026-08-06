package client

import (
	"context"
	"time"

	"github.com/backpack/backpack/internal/utils"

	"github.com/backpack/backpack/config"

	"github.com/backpack/backpack/internal/client/transport"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/utils/network"

	"net/http"
	_ "net/http/pprof"

	"github.com/sirupsen/logrus"
)

// Client encapsulates the client configuration and state
type Client struct {
	config *config.ClientConfig
	ctx    context.Context
	cancel context.CancelFunc
	logger *logrus.Logger
}

func NewClient(cfg *config.ClientConfig, parentCtx context.Context) *Client {
	ctx, cancel := context.WithCancel(parentCtx)
	// One process runs one tunnel, so the socket tuning is process-wide.
	network.SetPinTCPBuffers(cfg.SOPinTCP)
	// Off unless this tunnel asked for it; see handlers/zerocopy.go.
	// Off unless this tunnel asked for it; see handlers/zerocopy.go.
	handlers.SetZeroCopy(cfg.ZeroCopy)
	return &Client{
		config: cfg,
		ctx:    ctx,
		cancel: cancel,
		logger: utils.NewLoggerWithFormat(cfg.LogLevel, cfg.LogFormat),
	}
}

// Run starts the client and begins dialing the tunnel server
func (c *Client) Start() {
	// Profiling endpoint, off unless explicitly enabled in the config. Bound to
	// loopback: pprof is unauthenticated and its heap dump would expose the
	// tunnel token. Reach it with `ssh -L 6061:127.0.0.1:6061 root@server`.
	if c.config.PPROF {
		go func() {
			c.logger.Info("pprof listening on 127.0.0.1:6061 (loopback only)")
			if err := http.ListenAndServe("127.0.0.1:6061", nil); err != nil {
				c.logger.Errorf("pprof server stopped: %v", err)
			}
		}()
	}

	c.logger.Infof("client with remote address %s started successfully", c.config.RemoteAddr)

	// One rotating endpoint list shared by every transport: the primary
	// address plus any fallbacks, so a filtered IP or blocked port is
	// retried against the next option instead of stalling the tunnel.
	endpoints := network.NewEndpoints(c.config.RemoteAddr, c.config.FallbackAddrs...)
	if endpoints.Len() > 1 {
		c.logger.Infof("%d server endpoints configured (failover enabled)", endpoints.Len())
	}
	// With balancing on, new data connections are spread over every endpoint
	// rather than all following the control channel.
	if c.config.LoadBalance && endpoints.Len() > 1 {
		endpoints.SetSpread(true)
		c.logger.Infof("load balancing enabled across %d endpoints", endpoints.Len())
	}

	// Built once for every transport that can use it. The configuration was
	// already validated at load time, so a failure here would mean the file
	// changed underneath us; dialling directly is the safe reading, and it is
	// said out loud rather than assumed.
	outbound, err := buildOutbound(c.config)
	if err != nil {
		c.logger.Errorf("ignoring the configured outbound settings and dialling directly: %v", err)
		outbound = nil
	}
	if outbound.IsSet() {
		c.logger.Infof("reaching the tunnel server %s", outbound)
	}

	switch c.config.Transport {
	case config.TCP, config.STEALTH:
		tcpConfig := &transport.TcpConfig{
			RemoteAddr:     c.config.RemoteAddr,
			Endpoints:      endpoints,
			Nodelay:        c.config.Nodelay,
			KeepAlive:      time.Duration(c.config.Keepalive) * time.Second,
			RetryInterval:  time.Duration(c.config.RetryInterval) * time.Second,
			DialTimeOut:    time.Duration(c.config.DialTimeout) * time.Second,
			ConnPoolSize:   c.config.ConnectionPool,
			Token:          c.config.Token,
			Sniffer:        c.config.Sniffer,
			WebPort:        c.config.WebPort,
			SnifferLog:     c.config.SnifferLog,
			AggressivePool: c.config.AggressivePool,
			MSS:            c.config.MSS,
			SO_RCVBUF:      c.config.SO_RCVBUF,
			SO_SNDBUF:      c.config.SO_SNDBUF,
			Outbound:       outbound,
			// Stealth is the TCP transport with a Noise record layer over every
			// tunnel connection; everything else about it is identical.
			Stealth: c.config.Transport == config.STEALTH,
		}
		tcpClient := transport.NewTCPClient(c.ctx, tcpConfig, c.logger)
		go tcpClient.Start()

	case config.TCPMUX:
		tcpMuxConfig := &transport.TcpMuxConfig{
			RemoteAddr:       c.config.RemoteAddr,
			Endpoints:        endpoints,
			Nodelay:          c.config.Nodelay,
			KeepAlive:        time.Duration(c.config.Keepalive) * time.Second,
			RetryInterval:    time.Duration(c.config.RetryInterval) * time.Second,
			DialTimeOut:      time.Duration(c.config.DialTimeout) * time.Second,
			ConnPoolSize:     c.config.ConnectionPool,
			Token:            c.config.Token,
			MuxVersion:       c.config.MuxVersion,
			MaxFrameSize:     c.config.MaxFrameSize,
			MaxReceiveBuffer: c.config.MaxReceiveBuffer,
			MaxStreamBuffer:  c.config.MaxStreamBuffer,
			Sniffer:          c.config.Sniffer,
			WebPort:          c.config.WebPort,
			SnifferLog:       c.config.SnifferLog,
			AggressivePool:   c.config.AggressivePool,
			MSS:              c.config.MSS,
			SO_RCVBUF:        c.config.SO_RCVBUF,
			SO_SNDBUF:        c.config.SO_SNDBUF,
			Outbound:         outbound,
		}
		tcpMuxClient := transport.NewMuxClient(c.ctx, tcpMuxConfig, c.logger)
		go tcpMuxClient.Start()

	case config.KCP, config.XDI:
		kcp := c.config.KCPConfig.WithDefaults()
		useICMP := c.config.Transport == config.XDI
		kcpConfig := &transport.KcpConfig{
			RemoteAddr:       c.config.RemoteAddr,
			Endpoints:        endpoints,
			KeepAlive:        time.Duration(c.config.Keepalive) * time.Second,
			RetryInterval:    time.Duration(c.config.RetryInterval) * time.Second,
			DialTimeOut:      time.Duration(c.config.DialTimeout) * time.Second,
			ConnPoolSize:     c.config.ConnectionPool,
			Token:            c.config.Token,
			MuxVersion:       c.config.MuxVersion,
			MaxFrameSize:     c.config.MaxFrameSize,
			MaxReceiveBuffer: c.config.MaxReceiveBuffer,
			MaxStreamBuffer:  c.config.MaxStreamBuffer,
			Sniffer:          c.config.Sniffer,
			WebPort:          c.config.WebPort,
			SnifferLog:       c.config.SnifferLog,
			AggressivePool:   c.config.AggressivePool,
			SO_RCVBUF:        c.config.SO_RCVBUF,
			SO_SNDBUF:        c.config.SO_SNDBUF,
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
		}
		kcpClient := transport.NewKcpClient(c.ctx, kcpConfig, c.logger)
		go kcpClient.Start()

	case config.QUIC:
		quicConfig := &transport.QuicConfig{
			RemoteAddr:     c.config.RemoteAddr,
			Endpoints:      endpoints,
			KeepAlive:      time.Duration(c.config.Keepalive) * time.Second,
			RetryInterval:  time.Duration(c.config.RetryInterval) * time.Second,
			DialTimeOut:    time.Duration(c.config.DialTimeout) * time.Second,
			ConnPoolSize:   c.config.ConnectionPool,
			Token:          c.config.Token,
			Sniffer:        c.config.Sniffer,
			WebPort:        c.config.WebPort,
			SnifferLog:     c.config.SnifferLog,
			AggressivePool: c.config.AggressivePool,
			SO_RCVBUF:      c.config.SO_RCVBUF,
			SO_SNDBUF:      c.config.SO_SNDBUF,
		}
		quicClient := transport.NewQuicClient(c.ctx, quicConfig, c.logger)
		go quicClient.Start()

	case config.WS, config.WSS:
		WsConfig := &transport.WsConfig{
			RemoteAddr:     c.config.RemoteAddr,
			Endpoints:      endpoints,
			Nodelay:        c.config.Nodelay,
			KeepAlive:      time.Duration(c.config.Keepalive) * time.Second,
			RetryInterval:  time.Duration(c.config.RetryInterval) * time.Second,
			DialTimeOut:    time.Duration(c.config.DialTimeout) * time.Second,
			ConnPoolSize:   c.config.ConnectionPool,
			Token:          c.config.Token,
			Sniffer:        c.config.Sniffer,
			WebPort:        c.config.WebPort,
			SnifferLog:     c.config.SnifferLog,
			Mode:           c.config.Transport,
			SimpleAuth:     c.config.SimpleAuth,
			AggressivePool: c.config.AggressivePool,
			EdgeIP:         c.config.EdgeIP,
			Outbound:       outbound,
		}
		WsClient := transport.NewWSClient(c.ctx, WsConfig, c.logger)
		go WsClient.Start()

	case config.WSMUX, config.WSSMUX:
		wsMuxConfig := &transport.WsMuxConfig{
			RemoteAddr:       c.config.RemoteAddr,
			Endpoints:        endpoints,
			Nodelay:          c.config.Nodelay,
			KeepAlive:        time.Duration(c.config.Keepalive) * time.Second,
			RetryInterval:    time.Duration(c.config.RetryInterval) * time.Second,
			DialTimeOut:      time.Duration(c.config.DialTimeout) * time.Second,
			ConnPoolSize:     c.config.ConnectionPool,
			Token:            c.config.Token,
			MuxVersion:       c.config.MuxVersion,
			MaxFrameSize:     c.config.MaxFrameSize,
			MaxReceiveBuffer: c.config.MaxReceiveBuffer,
			MaxStreamBuffer:  c.config.MaxStreamBuffer,
			Sniffer:          c.config.Sniffer,
			WebPort:          c.config.WebPort,
			SnifferLog:       c.config.SnifferLog,
			Mode:             c.config.Transport,
			SimpleAuth:       c.config.SimpleAuth,
			AggressivePool:   c.config.AggressivePool,
			EdgeIP:           c.config.EdgeIP,
			Outbound:         outbound,
		}
		wsMuxClient := transport.NewWSMuxClient(c.ctx, wsMuxConfig, c.logger)
		go wsMuxClient.Start()

	case config.UDP:
		udpConfig := &transport.UdpConfig{
			RemoteAddr:     c.config.RemoteAddr,
			Endpoints:      endpoints,
			RetryInterval:  time.Duration(c.config.RetryInterval) * time.Second,
			DialTimeOut:    time.Duration(c.config.DialTimeout) * time.Second,
			ConnPoolSize:   c.config.ConnectionPool,
			Token:          c.config.Token,
			Sniffer:        c.config.Sniffer,
			WebPort:        c.config.WebPort,
			SnifferLog:     c.config.SnifferLog,
			AggressivePool: c.config.AggressivePool,
			SO_RCVBUF:      c.config.SO_RCVBUF,
			SO_SNDBUF:      c.config.SO_SNDBUF,
		}
		udpClient := transport.NewUDPClient(c.ctx, udpConfig, c.logger)
		go udpClient.Start()

	default:
		c.logger.Fatal("invalid transport type: ", c.config.Transport)
	}

	<-c.ctx.Done()

	c.logger.Info("all workers stopped successfully")

	// suppress other logs
	c.logger.SetLevel(logrus.FatalLevel)
}
func (c *Client) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
}

// buildOutbound turns the client's configuration into the description the
// dialer wants. A configuration with none of it set yields nil, which is the
// ordinary "dial directly" case and costs nothing to carry.
func buildOutbound(cfg *config.ClientConfig) (*network.Outbound, error) {
	proxy, err := network.ParseProxy(cfg.Proxy)
	if err != nil {
		return nil, err
	}
	out := &network.Outbound{
		Proxy:     proxy,
		LocalAddr: cfg.LocalAddr,
		Interface: cfg.Interface,
		Mark:      cfg.SOMark,
	}
	if !out.IsSet() {
		return nil, nil
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}
