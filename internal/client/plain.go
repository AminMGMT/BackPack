package client

import (
	"context"
	"time"

	"github.com/backpack/backpack/config"
	plain "github.com/backpack/backpack/internal/plain/client"
)

// usesPlainEngine reports whether tr runs on the plain engine — Backhaul's
// tcp, tcpmux, ws, wss, wsmux and wssmux, ported in internal/plain. tcp under
// a managed HTTPS carrier stays on Backpack's own tcp transport, as stealth
// does (see the server's copy of this).
func usesPlainEngine(tr config.TransportType, managed bool) bool {
	switch tr {
	case config.TCP:
		return !managed
	case config.TCPMUX, config.WS, config.WSS, config.WSMUX, config.WSSMUX:
		return true
	}
	return false
}

// startPlain starts tr on the plain engine.
func (c *Client) startPlain(ctx context.Context, tr config.TransportType) runner {
	cfg := c.config
	keepAlive := time.Duration(cfg.Keepalive) * time.Second
	retry := time.Duration(cfg.RetryInterval) * time.Second
	dialTimeout := time.Duration(cfg.DialTimeout) * time.Second

	// Backhaul's own default where none is set: version 1. Backpack's config
	// leaves 0 (negotiate), which this engine has no handshake to settle.
	muxVersion := cfg.MuxVersion
	if muxVersion != 1 && muxVersion != 2 {
		muxVersion = 1
	}

	switch tr {
	case config.TCP:
		t := plain.NewTCPClient(ctx, &plain.TcpConfig{
			RemoteAddr:     cfg.RemoteAddr,
			Nodelay:        cfg.Nodelay,
			KeepAlive:      keepAlive,
			RetryInterval:  retry,
			DialTimeOut:    dialTimeout,
			ConnPoolSize:   cfg.ConnectionPool,
			Token:          cfg.Token,
			Sniffer:        cfg.Sniffer,
			WebPort:        cfg.WebPort,
			SnifferLog:     cfg.SnifferLog,
			AggressivePool: cfg.AggressivePool,
			MSS:            cfg.MSS,
			SO_RCVBUF:      cfg.SO_RCVBUF,
			SO_SNDBUF:      cfg.SO_SNDBUF,
		}, c.logger)
		go t.Start()
		return t

	case config.TCPMUX:
		t := plain.NewMuxClient(ctx, &plain.TcpMuxConfig{
			RemoteAddr:       cfg.RemoteAddr,
			Nodelay:          cfg.Nodelay,
			KeepAlive:        keepAlive,
			RetryInterval:    retry,
			DialTimeOut:      dialTimeout,
			ConnPoolSize:     cfg.ConnectionPool,
			Token:            cfg.Token,
			MuxVersion:       muxVersion,
			MaxFrameSize:     cfg.MaxFrameSize,
			MaxReceiveBuffer: cfg.MaxReceiveBuffer,
			MaxStreamBuffer:  cfg.MaxStreamBuffer,
			Sniffer:          cfg.Sniffer,
			WebPort:          cfg.WebPort,
			SnifferLog:       cfg.SnifferLog,
			AggressivePool:   cfg.AggressivePool,
			MSS:              cfg.MSS,
			SO_RCVBUF:        cfg.SO_RCVBUF,
			SO_SNDBUF:        cfg.SO_SNDBUF,
		}, c.logger)
		go t.Start()
		return t

	case config.WS, config.WSS:
		t := plain.NewWSClient(ctx, &plain.WsConfig{
			RemoteAddr:     cfg.RemoteAddr,
			Nodelay:        cfg.Nodelay,
			KeepAlive:      keepAlive,
			RetryInterval:  retry,
			DialTimeOut:    dialTimeout,
			ConnPoolSize:   cfg.ConnectionPool,
			Token:          cfg.Token,
			Sniffer:        cfg.Sniffer,
			WebPort:        cfg.WebPort,
			SnifferLog:     cfg.SnifferLog,
			Mode:           tr,
			AggressivePool: cfg.AggressivePool,
			EdgeIP:         cfg.EdgeIP,
		}, c.logger)
		go t.Start()
		return t

	case config.WSMUX, config.WSSMUX:
		t := plain.NewWSMuxClient(ctx, &plain.WsMuxConfig{
			RemoteAddr:       cfg.RemoteAddr,
			Nodelay:          cfg.Nodelay,
			KeepAlive:        keepAlive,
			RetryInterval:    retry,
			DialTimeOut:      dialTimeout,
			ConnPoolSize:     cfg.ConnectionPool,
			Token:            cfg.Token,
			MuxVersion:       muxVersion,
			MaxFrameSize:     cfg.MaxFrameSize,
			MaxReceiveBuffer: cfg.MaxReceiveBuffer,
			MaxStreamBuffer:  cfg.MaxStreamBuffer,
			Sniffer:          cfg.Sniffer,
			WebPort:          cfg.WebPort,
			SnifferLog:       cfg.SnifferLog,
			Mode:             tr,
			AggressivePool:   cfg.AggressivePool,
			EdgeIP:           cfg.EdgeIP,
		}, c.logger)
		go t.Start()
		return t
	}
	return nil
}
