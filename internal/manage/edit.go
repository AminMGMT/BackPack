package manage

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

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
		FallbackAddrs:   cc.FallbackAddrs,
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
	return applySpec(s)
}

// SetFallbackAddrs replaces the list of backup server addresses on a client
// tunnel. Each entry is a full "host:port" (or "host" — the primary port is
// assumed). When the primary address stops answering, the client walks this
// list until one connects, which keeps the tunnel alive after a server IP gets
// filtered, a port gets blocked, or when you want to fail over to a CDN edge.
//
// Passing an empty list clears the fallbacks.
func SetFallbackAddrs(name string, addrs []string) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	if s.Role != "client" {
		return fmt.Errorf("fallback addresses apply to client tunnels only")
	}

	primaryPort := addrPort(s.RemoteAddr)
	var clean []string
	seen := map[string]bool{strings.TrimSpace(s.RemoteAddr): true}
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		// Accept a bare host/IP by reusing the primary's port.
		host, port := a, ""
		if h, p, err := net.SplitHostPort(a); err == nil {
			host, port = h, p
		} else if strings.Count(a, ":") > 1 && !strings.HasPrefix(a, "[") {
			host, port = a, "" // bare IPv6 literal
		}
		if port == "" {
			if primaryPort == "" {
				return fmt.Errorf("%q has no port and the primary address has none either", a)
			}
			port = primaryPort
		}
		if !validPort(port) {
			return fmt.Errorf("invalid port in %q", a)
		}
		if host == "" {
			return fmt.Errorf("invalid address %q", a)
		}
		if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
			host = "[" + host + "]" // IPv6 literal
		}
		full := net.JoinHostPort(strings.Trim(host, "[]"), port)
		if seen[full] {
			continue
		}
		seen[full] = true
		clean = append(clean, full)
	}

	s.FallbackAddrs = clean
	return applySpec(s)
}

// ChangeTransport switches an existing tunnel to a different carrier (tcp,
// tcpmux, udp, ws, wss, wsmux, wssmux) without touching its name, token or
// forwarded ports — so the peer keeps the same credentials and services. A
// wss/wssmux server gets a self-signed certificate generated automatically if
// it doesn't have one yet. The change is verified and auto-reverted on failure.
//
// Both ends must use the same transport, so the peer has to be switched too.
func ChangeTransport(name, transport string) error {
	transport = strings.ToLower(strings.TrimSpace(transport))
	if !validTransport(transport) {
		return fmt.Errorf("unknown transport %q", transport)
	}
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	if s.Transport == transport {
		return fmt.Errorf("this tunnel already uses %s", transport)
	}
	s.Transport = transport

	// Mux transports need their SMUX knobs populated; a tunnel created as plain
	// TCP has them at zero, which the engine would reject or run badly.
	if isMux(transport) && s.MuxCon <= 0 {
		s.MuxCon = 8
		s.MuxVersion = 2
		s.MuxFrameSize = 32768
		s.MuxRecvBuffer = 4194304
		s.MuxStreamBuffer = 65536
	}
	// TLS transports need a certificate on the server side.
	if s.Role == "server" && needsTLS(transport) && (s.TLSCert == "" || !fileExists(s.TLSCert)) {
		cert, key, err := EnsureSelfSignedCert(s.Name, "")
		if err != nil {
			return fmt.Errorf("could not generate a TLS certificate: %w", err)
		}
		s.TLSCert, s.TLSKey = cert, key
	}
	// accept_udp is only meaningful on the plain TCP transport.
	if transport != "tcp" {
		s.AcceptUDP = false
	}
	return applySpec(s)
}

// applySpec writes a changed tunnel config, restarts the service and verifies
// it actually came back up. If it does not, the previous config is put back and
// the tunnel restarted with it, so a bad edit (a port already in use, a wrong
// address) can never leave the user with a dead tunnel and a lost config.
func applySpec(s TunnelSpec) error {
	path := app.ConfigPath(s.Name)
	service := app.ServiceName(s.Name)

	prev, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("could not read the current config: %w", err)
	}
	wasActive := IsActive(service)

	if _, err := s.Save(); err != nil {
		// Save failed — put the original file back untouched.
		_ = os.WriteFile(path, prev, 0644)
		return err
	}
	// Save alone won't reload an already-running unit — restart explicitly.
	if err := RestartService(service); err != nil {
		revertSpec(path, prev, service, wasActive)
		return fmt.Errorf("the tunnel failed to restart with the new settings — reverted: %w", err)
	}

	// The unit can report "activating" for a moment; give it a chance, then
	// confirm it is really running.
	if !WaitServiceActive(service, 10*time.Second) {
		detail := lastLogLine(service)
		revertSpec(path, prev, service, wasActive)
		if detail != "" {
			return fmt.Errorf("the tunnel did not come up with the new settings — reverted. Reason: %s", detail)
		}
		return fmt.Errorf("the tunnel did not come up with the new settings — reverted to the previous config")
	}
	return nil
}

// revertSpec restores a previous config file and brings the service back to the
// state it was in before the edit.
func revertSpec(path string, prev []byte, service string, wasActive bool) {
	_ = os.WriteFile(path, prev, 0644)
	if wasActive {
		_ = RestartService(service)
	} else {
		_ = StopService(service)
	}
}

// lastLogLine returns the most recent meaningful journal line for a service,
// used to explain why an edit was reverted.
func lastLogLine(service string) string {
	out, err := exec.Command("journalctl", "-u", service, "-n", "12", "--no-pager", "-o", "cat").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		low := strings.ToLower(l)
		if strings.Contains(low, "error") || strings.Contains(low, "fatal") ||
			strings.Contains(low, "failed") || strings.Contains(low, "in use") {
			return l
		}
	}
	return ""
}
