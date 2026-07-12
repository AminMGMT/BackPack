package manage

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/optimize"
)

// CreateServerTunnel builds and starts a best-performance server tunnel from the
// minimal parameters the web UI collects. It generates a 64-char token and
// returns it. This mirrors the CLI "Setup Server" flow.
// For wss/wssmux: pass tlsCert/tlsKey paths to use an existing certificate, or
// leave both empty to auto-generate a self-signed pair (tunnel clients skip
// certificate verification, so self-signed works out of the box).
func CreateServerTunnel(name, port, transport string, ports []string, ipv6 bool, country string, socksPort int, tlsCert, tlsKey string) (string, error) {
	name = strings.TrimSpace(name)
	port = strings.TrimSpace(port)
	if !validPort(port) {
		return "", fmt.Errorf("invalid tunnel port")
	}
	if name == "" {
		name = "server-" + port
	}
	if fileExists(app.ConfigPath(name)) {
		return "", fmt.Errorf("a tunnel named %q already exists", name)
	}
	if !validTransport(transport) {
		return "", fmt.Errorf("unknown transport %q", transport)
	}

	var clean []string
	for _, p := range ports {
		if p = strings.TrimSpace(p); p != "" {
			clean = append(clean, p)
		}
	}
	if len(clean) == 0 {
		return "", fmt.Errorf("at least one forwarded port is required")
	}

	// Optional custom SOCKS relay port (for the Telegram bot). Maps the chosen
	// public port to the peer's built-in SOCKS5 proxy.
	if socksPort > 0 {
		if socksPort < 1 || socksPort > 65535 {
			return "", fmt.Errorf("invalid SOCKS5 port")
		}
		clean = append(clean, fmt.Sprintf("%d=127.0.0.1:%d", socksPort, app.SocksInternalPort))
	}

	bind := "0.0.0.0"
	if ipv6 {
		bind = "[::]"
	}

	token := randomToken(64)
	s := TunnelSpec{
		Role:      "server",
		Transport: transport,
		BindAddr:  bind + ":" + port,
		Name:      name,
		Token:     token,
		Ports:     clean,
	}

	if needsTLS(transport) {
		tlsCert, tlsKey = strings.TrimSpace(tlsCert), strings.TrimSpace(tlsKey)
		if tlsCert == "" && tlsKey == "" {
			var err error
			if tlsCert, tlsKey, err = EnsureSelfSignedCert(name, ""); err != nil {
				return "", fmt.Errorf("could not generate TLS certificate: %v", err)
			}
		} else if err := validCertPair(tlsCert, tlsKey); err != nil {
			return "", err
		}
		s.TLSCert, s.TLSKey = tlsCert, tlsKey
	}

	bestPerfPreset(&s)

	optimize.ApplyQuiet()
	if _, err := s.Save(); err != nil {
		return "", err
	}
	SetTunnelCountry(name, strings.ToUpper(strings.TrimSpace(country)))
	return token, nil
}

// CreateClientTunnel builds and starts a best-performance client tunnel from
// the parameters the web UI collects. It mirrors the CLI "Setup Client" flow:
// the token must be the one generated on the server side. edgeIP is optional
// and only meaningful for the websocket transports.
func CreateClientTunnel(name, host, port, transport, token, edgeIP, country string) error {
	name = strings.TrimSpace(name)
	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	token = strings.TrimSpace(token)
	if host == "" || !validPort(port) {
		return fmt.Errorf("invalid server address or port")
	}
	if token == "" {
		return fmt.Errorf("the server token is required")
	}
	if name == "" {
		name = "client-" + port
	}
	if fileExists(app.ConfigPath(name)) {
		return fmt.Errorf("a tunnel named %q already exists", name)
	}
	if !validTransport(transport) {
		return fmt.Errorf("unknown transport %q", transport)
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]" // IPv6 literal
	}

	s := TunnelSpec{
		Role:       "client",
		Transport:  transport,
		RemoteAddr: host + ":" + port,
		Name:       name,
		Token:      token,
	}
	if isWS(transport) {
		s.EdgeIP = strings.TrimSpace(edgeIP)
	}
	bestPerfPreset(&s)

	optimize.ApplyQuiet()
	if _, err := s.Save(); err != nil {
		return err
	}
	SetTunnelCountry(name, strings.ToUpper(strings.TrimSpace(country)))
	return nil
}

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

// EnsureSocksPort makes sure the given server tunnel exposes a port that maps to
// the peer's local SOCKS5 proxy (127.0.0.1:SocksInternalPort), so this node can
// reach the internet through the peer. It returns the exposed port, adding the
// mapping (and restarting the tunnel) if it isn't already present.
func EnsureSocksPort(name string) (int, error) {
	spec, err := loadServerSpec(name)
	if err != nil {
		return 0, err
	}
	suffix := fmt.Sprintf("=127.0.0.1:%d", app.SocksInternalPort)
	for _, p := range spec.Ports {
		if strings.HasSuffix(p, suffix) {
			if n, e := strconv.Atoi(strings.TrimSuffix(p, suffix)); e == nil {
				return n, nil
			}
		}
	}
	r := randomHighPort()
	spec.Ports = append(spec.Ports, fmt.Sprintf("%d%s", r, suffix))
	if _, err := spec.Save(); err != nil {
		return 0, err
	}
	// Restart so the new forwarded port takes effect.
	RestartService(app.ServiceName(name))
	return r, nil
}

// randomHighPort returns a random ephemeral-ish port in [20000, 60000).
func randomHighPort() int {
	n, err := rand.Int(rand.Reader, big.NewInt(40000))
	if err != nil {
		return 45678
	}
	return 20000 + int(n.Int64())
}

// TunnelAction runs a lifecycle action on a tunnel by name.
func TunnelAction(name, action string) error {
	name = strings.TrimSpace(name)
	if !fileExists(app.ConfigPath(name)) {
		return fmt.Errorf("no such tunnel")
	}
	service := app.ServiceName(name)
	switch action {
	case "start":
		return StartService(service)
	case "stop":
		return StopService(service)
	case "restart":
		return RestartService(service)
	case "delete":
		return Delete(name)
	default:
		return fmt.Errorf("unknown action %q", action)
	}
}
