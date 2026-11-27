package client

import (
	"context"
	"github.com/backpack/backpack/internal/plain/core"
	"time"

	"github.com/backpack/backpack/config"
	plain "github.com/backpack/backpack/internal/plain/client"
)

// usesPlainEngine reports whether tr runs on the plain engine — its
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
// onLegacy is called when the Iran server turns out to run Backpack v2.0.0;
// see compat.go.
func (c *Client) startPlain(ctx context.Context, tr config.TransportType, route *core.Route, onLegacy func()) runner {
	cfg := c.config
	keepAlive := time.Duration(cfg.Keepalive) * time.Second
	retry := time.Duration(cfg.RetryInterval) * time.Second
	dialTimeout := time.Duration(cfg.DialTimeout) * time.Second

	// The plain engine's own default where none is set: version 1. Backpack's config
	// leaves 0 (negotiate), which this engine has no handshake to settle.
	muxVersion := cfg.MuxVersion
	if muxVersion != 1 && muxVersion != 2 {
		muxVersion = 1
	}

	// Backpack: the tunnel's encryption; nil (none) leaves the plain wire.
	layer := core.NewLayer(cfg.EncryptionConfig, cfg.Token).ForHost(cfg.RemoteAddr)

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
			OnLegacyServer: onLegacy,
			Route:          route,
			Layer:          layer,
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
			OnLegacyServer:   onLegacy,
			Route:            route,
			Layer:            layer,
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
			OnLegacyServer: onLegacy,
			Route:          route,
			Layer:          layer,
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
			OnLegacyServer:   onLegacy,
			Route:            route,
			Layer:            layer,
		}, c.logger)
		go t.Start()
		return t
	}
	return nil
}
