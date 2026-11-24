package server

import (
	"context"
	"time"

	"github.com/backpack/backpack/config"
	plain "github.com/backpack/backpack/internal/plain/server"
)

// usesPlainEngine reports whether tr runs on the plain engine — Backhaul's
// tcp, tcpmux, ws, wss, wsmux and wssmux, ported in internal/plain.
//
// tcp under a managed HTTPS carrier (naive, xhttp, REALITY) is the exception:
// that carrier relies on the directional-EOF framing of Backpack's own tcp
// transport, which keeps running it, as it keeps running stealth.
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
func (s *Server) startPlain(ctx context.Context, tr config.TransportType) runner {
	c := s.config
	keepAlive := time.Duration(c.Keepalive) * time.Second
	heartbeat := time.Duration(c.Heartbeat) * time.Second

	// Backhaul's own default where none is set: version 1. Backpack's config
	// leaves 0 (negotiate), which this engine has no handshake to settle.
	muxVersion := c.MuxVersion
	if muxVersion != 1 && muxVersion != 2 {
		muxVersion = 1
	}

	switch tr {
	case config.TCP:
		t := plain.NewTCPServer(ctx, &plain.TcpConfig{
			BindAddr:      c.BindAddr,
			Nodelay:       c.Nodelay,
			KeepAlive:     keepAlive,
			Heartbeat:     heartbeat,
			Token:         c.Token,
			ChannelSize:   c.ChannelSize,
			Ports:         c.Ports,
			Sniffer:       c.Sniffer,
			WebPort:       c.WebPort,
			SnifferLog:    c.SnifferLog,
			AcceptUDP:     c.ForwardsUDP(),
			MSS:           c.MSS,
			SO_RCVBUF:     c.SO_RCVBUF,
			SO_SNDBUF:     c.SO_SNDBUF,
			ProxyProtocol: c.ProxyProtocol,
		}, s.logger)
		go t.Start()
		return t

	case config.TCPMUX:
		t := plain.NewTcpMuxServer(ctx, &plain.TcpMuxConfig{
			BindAddr:         c.BindAddr,
			Nodelay:          c.Nodelay,
			KeepAlive:        keepAlive,
			Heartbeat:        heartbeat,
			Token:            c.Token,
			ChannelSize:      c.ChannelSize,
			Ports:            c.Ports,
			MuxCon:           c.MuxCon,
			MuxVersion:       muxVersion,
			MaxFrameSize:     c.MaxFrameSize,
			MaxReceiveBuffer: c.MaxReceiveBuffer,
			MaxStreamBuffer:  c.MaxStreamBuffer,
			Sniffer:          c.Sniffer,
			WebPort:          c.WebPort,
			SnifferLog:       c.SnifferLog,
			MSS:              c.MSS,
			SO_RCVBUF:        c.SO_RCVBUF,
			SO_SNDBUF:        c.SO_SNDBUF,
			ProxyProtocol:    c.ProxyProtocol,
		}, s.logger)
		go t.Start()
		return t

	case config.WS, config.WSS:
		t := plain.NewWSServer(ctx, &plain.WsConfig{
			BindAddr:     c.BindAddr,
			Nodelay:      c.Nodelay,
			KeepAlive:    keepAlive,
			Heartbeat:    heartbeat,
			Token:        c.Token,
			ChannelSize:  c.ChannelSize,
			Ports:        c.Ports,
			Sniffer:      c.Sniffer,
			WebPort:      c.WebPort,
			SnifferLog:   c.SnifferLog,
			Mode:         tr,
			TLSCertFile:  c.TLSCertFile,
			TLSKeyFile:   c.TLSKeyFile,
			ACMEDomain:   c.ACMEDomain,
			ACMEEmail:    c.ACMEEmail,
			ACMECacheDir: acmeCacheDir,
		}, s.logger)
		go t.Start()
		return t

	case config.WSMUX, config.WSSMUX:
		t := plain.NewWSMuxServer(ctx, &plain.WsMuxConfig{
			BindAddr:         c.BindAddr,
			Nodelay:          c.Nodelay,
			KeepAlive:        keepAlive,
			Heartbeat:        heartbeat,
			Token:            c.Token,
			ChannelSize:      c.ChannelSize,
			Ports:            c.Ports,
			MuxCon:           c.MuxCon,
			MuxVersion:       muxVersion,
			MaxFrameSize:     c.MaxFrameSize,
			MaxReceiveBuffer: c.MaxReceiveBuffer,
			MaxStreamBuffer:  c.MaxStreamBuffer,
			Sniffer:          c.Sniffer,
			WebPort:          c.WebPort,
			SnifferLog:       c.SnifferLog,
			Mode:             tr,
			TLSCertFile:      c.TLSCertFile,
			TLSKeyFile:       c.TLSKeyFile,
			ACMEDomain:       c.ACMEDomain,
			ACMEEmail:        c.ACMEEmail,
			ACMECacheDir:     acmeCacheDir,
			ProxyProtocol:    c.ProxyProtocol,
		}, s.logger)
		go t.Start()
		return t
	}
	return nil
}
