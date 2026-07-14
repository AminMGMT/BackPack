package manage

import (
	"fmt"
	"net"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
)

// loadServerSpec reconstructs a server tunnel's spec from its config file so it
// can be modified and re-saved without losing settings.
func loadServerSpec(name string) (TunnelSpec, error) {
	var cfg config.Config
	if _, err := toml.DecodeFile(app.ConfigPath(name), &cfg); err != nil {
		return TunnelSpec{}, err
	}
	sc := cfg.Server
	if sc.BindAddr == "" {
		return TunnelSpec{}, fmt.Errorf("%q is not a server tunnel", name)
	}
	return TunnelSpec{
		Role:            "server",
		Name:            name,
		Transport:       string(sc.Transport),
		BindAddr:        sc.BindAddr,
		Token:           sc.Token,
		ChannelSize:     sc.ChannelSize,
		KeepAlive:       sc.Keepalive,
		Nodelay:         sc.Nodelay,
		Heartbeat:       sc.Heartbeat,
		LogLevel:        sc.LogLevel,
		AcceptUDP:       sc.AcceptUDP,
		Ports:           sc.Ports,
		MSS:             sc.MSS,
		SoRcvBuf:        sc.SO_RCVBUF,
		SoSndBuf:        sc.SO_SNDBUF,
		TLSCert:         sc.TLSCertFile,
		TLSKey:          sc.TLSKeyFile,
		MuxCon:          sc.MuxCon,
		MuxVersion:      sc.MuxVersion,
		MuxFrameSize:    sc.MaxFrameSize,
		MuxRecvBuffer:   sc.MaxReceiveBuffer,
		MuxStreamBuffer: sc.MaxStreamBuffer,
		Sniffer:         sc.Sniffer,
		WebPort:         sc.WebPort,
	}, nil
}

// loadClientSpec reconstructs a client tunnel's spec from its config file so it
// can be modified and re-saved without losing settings.
func loadClientSpec(name string) (TunnelSpec, error) {
	var cfg config.Config
	if _, err := toml.DecodeFile(app.ConfigPath(name), &cfg); err != nil {
		return TunnelSpec{}, err
	}
	cc := cfg.Client
	if cc.RemoteAddr == "" {
		return TunnelSpec{}, fmt.Errorf("%q is not a client tunnel", name)
	}
	return TunnelSpec{
		Role:            "client",
		Name:            name,
		Transport:       string(cc.Transport),
		RemoteAddr:      cc.RemoteAddr,
		Token:           cc.Token,
		ConnectionPool:  cc.ConnectionPool,
		AggressivePool:  cc.AggressivePool,
		KeepAlive:       cc.Keepalive,
		Nodelay:         cc.Nodelay,
		LogLevel:        cc.LogLevel,
		MSS:             cc.MSS,
		SoRcvBuf:        cc.SO_RCVBUF,
		SoSndBuf:        cc.SO_SNDBUF,
		EdgeIP:          cc.EdgeIP,
		MuxCon:          cc.MuxSession,
		MuxVersion:      cc.MuxVersion,
		MuxFrameSize:    cc.MaxFrameSize,
		MuxRecvBuffer:   cc.MaxReceiveBuffer,
		MuxStreamBuffer: cc.MaxStreamBuffer,
		Sniffer:         cc.Sniffer,
		WebPort:         cc.WebPort,
	}, nil
}

// LoadSpec reconstructs any tunnel's spec (server or client) from disk.
func LoadSpec(name string) (TunnelSpec, error) {
	if !fileExists(app.ConfigPath(name)) {
		return TunnelSpec{}, fmt.Errorf("no such tunnel %q", name)
	}
	if s, err := loadServerSpec(name); err == nil {
		return s, nil
	}
	return loadClientSpec(name)
}

// addrHost returns the host part of a host:port address (brackets stripped for
// IPv6), or fallback when it can't be parsed.
func addrHost(addr, fallback string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil && h != "" {
		return h
	}
	return fallback
}

// addrPort returns the port part of a host:port address, or "".
func addrPort(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return ""
}

// isBotRelayPort reports whether a port mapping is the hidden mapping to the
// peer's built-in SOCKS5 proxy (used for the Telegram relay).
func isBotRelayPort(p string) bool {
	return strings.HasSuffix(strings.TrimSpace(p), fmt.Sprintf("=127.0.0.1:%d", app.SocksInternalPort))
}

// VisiblePorts filters the hidden bot-relay mapping out of a forwarded-ports
// list, for display and editing.
func VisiblePorts(ports []string) []string {
	var out []string
	for _, p := range ports {
		if !isBotRelayPort(p) {
			out = append(out, p)
		}
	}
	return out
}

// EditTunnel changes a tunnel's ports/address in one shot and restarts it so
// the change takes effect. Empty values leave a setting unchanged:
//
//   - tunnelPort — the tunnel (control-channel) port: the local bind port on a
//     server, the remote server port on a client
//   - host — the server address (client tunnels only)
//   - ports — the full new forwarded-ports list (server tunnels only); the
//     hidden bot-relay mapping, if present, is preserved automatically
func EditTunnel(name, host, tunnelPort string, ports []string) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}

	changed := false
	if tunnelPort != "" || host != "" {
		if tunnelPort != "" && !validPort(tunnelPort) {
			return fmt.Errorf("invalid tunnel port %q", tunnelPort)
		}
		if s.Role == "server" {
			if host != "" {
				return fmt.Errorf("the address can only be changed on client tunnels")
			}
			s.BindAddr = net.JoinHostPort(addrHost(s.BindAddr, "0.0.0.0"), tunnelPort)
		} else {
			h := addrHost(s.RemoteAddr, "")
			p := addrPort(s.RemoteAddr)
			if host != "" {
				h = strings.Trim(strings.TrimSpace(host), "[]")
			}
			if tunnelPort != "" {
				p = tunnelPort
			}
			if h == "" || !validPort(p) {
				return fmt.Errorf("invalid server address or port")
			}
			s.RemoteAddr = net.JoinHostPort(h, p)
		}
		changed = true
	}

	if len(ports) > 0 {
		if s.Role != "server" {
			return fmt.Errorf("forwarded ports exist only on server tunnels")
		}
		var clean []string
		for _, p := range ports {
			if p = strings.TrimSpace(p); p != "" && !isBotRelayPort(p) {
				clean = append(clean, p)
			}
		}
		if len(clean) == 0 {
			return fmt.Errorf("at least one forwarded port is required")
		}
		if err := validatePortSpecs(clean); err != nil {
			return err
		}
		// Keep the hidden Telegram/SOCKS relay mapping the user never sees.
		for _, p := range s.Ports {
			if isBotRelayPort(p) {
				clean = append(clean, p)
			}
		}
		s.Ports = clean
		changed = true
	}

	if !changed {
		return fmt.Errorf("nothing to change")
	}
	if _, err := s.Save(); err != nil {
		return err
	}
	// Save alone won't reload an already-running unit — restart explicitly.
	return RestartService(app.ServiceName(s.Name))
}
