package tunnelspec

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage/core"
)

// LoadServer reconstructs a server tunnel's spec from its config file so it
// can be modified and re-saved without losing settings.
func LoadServer(name string) (Spec, error) {
	var cfg config.Config
	if _, err := toml.DecodeFile(app.ConfigPath(name), &cfg); err != nil {
		return Spec{}, err
	}
	sc := cfg.Server
	if sc.BindAddr == "" {
		return Spec{}, fmt.Errorf("%q is not a server tunnel", name)
	}
	return Spec{
		Role:               "server",
		Name:               name,
		Transport:          string(sc.Transport),
		BindAddr:           sc.BindAddr,
		Token:              sc.Token,
		ChannelSize:        sc.ChannelSize,
		KeepAlive:          sc.Keepalive,
		Nodelay:            sc.Nodelay,
		Heartbeat:          sc.Heartbeat,
		LogLevel:           sc.LogLevel,
		LogFormat:          sc.LogFormat,
		AcceptUDP:          sc.ForwardsUDP(),
		Ports:              sc.Ports,
		FallbackTransports: config.FallbackNames(sc.FallbackTransports),
		FallbackDwell:      sc.FallbackDwell,
		MSS:                sc.MSS,
		SoRcvBuf:           sc.SO_RCVBUF,
		SoSndBuf:           sc.SO_SNDBUF,
		SOPinTCP:           sc.SOPinTCP,
		TLSCert:            sc.TLSCertFile,
		TLSKey:             sc.TLSKeyFile,
		ACMEDomain:         sc.ACMEDomain,
		ACMEEmail:          sc.ACMEEmail,
		SimpleAuth:         sc.SimpleAuth,
		MuxCon:             sc.MuxCon,
		MuxVersion:         sc.MuxVersion,
		ZeroCopy:           sc.ZeroCopy,
		MuxFrameSize:       sc.MaxFrameSize,
		MuxRecvBuffer:      sc.MaxReceiveBuffer,
		MuxStreamBuffer:    sc.MaxStreamBuffer,
		Sniffer:            sc.Sniffer,
		WebPort:            sc.WebPort,
		WebBind:            sc.WebBind,
		ProxyProtocol:      sc.ProxyProtocol,
		MaxConnections:     sc.MaxConnections,
		BandwidthMbps:      sc.BandwidthMbps,
		Preset:             sc.Preset,
		KCPMTU:             sc.MTU,
		KCPInterval:        sc.Interval,
		KCPResend:          sc.Resend,
		KCPNoDelay:         sc.NoDelay,
		KCPNoCongestion:    sc.NoCongestion,
		KCPSndWnd:          sc.SndWnd,
		KCPRcvWnd:          sc.RcvWnd,
		KCPAckNoDelay:      sc.AckNoDelay,
		KCPDataShards:      sc.DataShards,
		KCPParityShards:    sc.ParityShards,
		PckInterface:       sc.PckInterface,
		PckGatewayMAC:      sc.PckGatewayMAC,
		PckFlags:           sc.PckFlags,
	}, nil
}

// loadClient reconstructs a client tunnel's spec from its config file so it
// can be modified and re-saved without losing settings.
func loadClient(name string) (Spec, error) {
	var cfg config.Config
	if _, err := toml.DecodeFile(app.ConfigPath(name), &cfg); err != nil {
		return Spec{}, err
	}
	cc := cfg.Client
	if cc.RemoteAddr == "" {
		return Spec{}, fmt.Errorf("%q is not a client tunnel", name)
	}
	return Spec{
		Role:               "client",
		Name:               name,
		Transport:          string(cc.Transport),
		RemoteAddr:         cc.RemoteAddr,
		FallbackAddrs:      cc.FallbackAddrs,
		FallbackTransports: config.FallbackNames(cc.FallbackTransports),
		FallbackDwell:      cc.FallbackDwell,
		Token:              cc.Token,
		ConnectionPool:     cc.ConnectionPool,
		AggressivePool:     cc.AggressivePool,
		DialTimeout:        cc.DialTimeout,
		RetryInterval:      cc.RetryInterval,
		SOPinTCP:           cc.SOPinTCP,
		KeepAlive:          cc.Keepalive,
		Nodelay:            cc.Nodelay,
		LogLevel:           cc.LogLevel,
		LogFormat:          cc.LogFormat,
		MSS:                cc.MSS,
		SoRcvBuf:           cc.SO_RCVBUF,
		SoSndBuf:           cc.SO_SNDBUF,
		EdgeIP:             cc.EdgeIP,
		SimpleAuth:         cc.SimpleAuth,
		Proxy:              cc.Proxy,
		LocalAddr:          cc.LocalAddr,
		Interface:          cc.Interface,
		SOMark:             cc.SOMark,
		ZeroCopy:           cc.ZeroCopy,
		MuxCon:             cc.MuxSession,
		MuxVersion:         cc.MuxVersion,
		MuxFrameSize:       cc.MaxFrameSize,
		MuxRecvBuffer:      cc.MaxReceiveBuffer,
		MuxStreamBuffer:    cc.MaxStreamBuffer,
		Sniffer:            cc.Sniffer,
		WebPort:            cc.WebPort,
		WebBind:            cc.WebBind,
		Preset:             cc.Preset,
		LoadBalance:        cc.LoadBalance,
		HealthFailover:     cc.HealthFailover,
		KCPMTU:             cc.MTU,
		KCPInterval:        cc.Interval,
		KCPResend:          cc.Resend,
		KCPNoDelay:         cc.NoDelay,
		KCPNoCongestion:    cc.NoCongestion,
		KCPSndWnd:          cc.SndWnd,
		KCPRcvWnd:          cc.RcvWnd,
		KCPAckNoDelay:      cc.AckNoDelay,
		KCPDataShards:      cc.DataShards,
		KCPParityShards:    cc.ParityShards,
		PckInterface:       cc.PckInterface,
		PckGatewayMAC:      cc.PckGatewayMAC,
		PckFlags:           cc.PckFlags,
	}, nil
}

// Load reconstructs any tunnel's spec (server or client) from disk.
func Load(name string) (Spec, error) {
	// Before the path is built, not after: see core.CheckName in validate.go.
	if err := core.CheckName(name); err != nil {
		return Spec{}, err
	}
	if !core.FileExists(app.ConfigPath(name)) {
		return Spec{}, fmt.Errorf("no such tunnel %q", name)
	}
	if s, err := LoadServer(name); err == nil {
		return s, nil
	}
	return loadClient(name)
}

// IsBotRelayPort reports whether a port mapping is the hidden mapping to the
// peer's built-in SOCKS5 proxy (used for the Telegram relay).
func IsBotRelayPort(p, token string) bool {
	p = strings.TrimSpace(p)
	// The Telegram forward, which the bot adds for itself.
	if IsTelegramPort(p) {
		return true
	}
	// The legacy fixed port, still present in configs written before the port
	// was derived from the token.
	if strings.HasSuffix(p, fmt.Sprintf("=127.0.0.1:%d", app.SocksInternalPort)) {
		return true
	}
	if token == "" {
		return false
	}
	return strings.HasSuffix(p, fmt.Sprintf("=127.0.0.1:%d", app.SocksPortForToken(token)))
}

// VisiblePorts filters the hidden bot-relay mapping out of a forwarded-ports
// list, for display and editing.
func VisiblePorts(ports []string, token string) []string {
	var out []string
	for _, p := range ports {
		if !IsBotRelayPort(p, token) {
			out = append(out, p)
		}
	}
	return out
}
