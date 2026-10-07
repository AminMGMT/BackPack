// Package naive manages optional HTTP/2 helpers under the existing reverse
// tunnel's context. All reverse protocol semantics remain in the TCP engine.
package naive

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/backpack/backpack/config"
)

func hostPort(addr string, loopback bool) (string, int, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, errors.New("expected a host:port address")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return "", 0, errors.New("port must be between 1 and 65535")
	}
	if loopback {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", 0, errors.New("the reverse target must be a literal loopback IP")
		}
	} else if host == "" {
		return "", 0, errors.New("host must be set explicitly")
	}
	return host, p, nil
}

func readableFile(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("file paths must be absolute")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("expected a regular file")
	}
	return nil
}

func credentials(binary, user, password string) error {
	if err := readableFile(binary); err != nil {
		return fmt.Errorf("helper binary: %w", err)
	}
	if user == "" || password == "" {
		return errors.New("username and password are required")
	}
	if strings.ContainsAny(user, ":\r\n") || strings.ContainsAny(password, "\r\n") {
		return errors.New("proxy credentials must not contain line breaks, and username must not contain a colon")
	}
	return nil
}

// Validate refuses helper settings for an inactive role: the engine selects
// the server first, so otherwise a client-only helper could be silently bypassed.
func Validate(c *config.Config) error {
	if !c.Server.Naive.Enabled() && !c.Client.Naive.Enabled() {
		return nil
	}
	if c.Direct.Enabled() || c.L3.Enabled() {
		return errors.New("Naive is a TCP Reverse wrapper, not a Direct or L3 carrier")
	}
	server, client := c.Server.BindAddr != "", c.Client.RemoteAddr != ""
	if server == client || c.Server.Naive.Enabled() != server || c.Client.Naive.Enabled() != client {
		return errors.New("Naive requires exactly one reverse role and helper settings for that same role")
	}
	if server {
		return ValidateServer(&c.Server)
	}
	return ValidateClient(&c.Client)
}

func ValidateClient(c *config.ClientConfig) error {
	n := c.Naive
	if !n.Enabled() {
		return nil
	}
	if c.Transport != config.TCP || len(c.FallbackTransports) != 0 {
		return errors.New("Naive currently supports only transport = tcp without transport fallbacks")
	}
	if c.Proxy != "" || c.LocalAddr != "" || c.Interface != "" || c.SOMark != 0 || len(c.FallbackAddrs) != 0 || c.HealthFailover || c.LoadBalance {
		return errors.New("Naive cannot be combined with proxy, routing bindings or endpoint failover/balancing")
	}
	if _, _, err := hostPort(c.RemoteAddr, true); err != nil {
		return fmt.Errorf("client.remote_addr: %w", err)
	}
	if _, _, err := hostPort(n.Server, false); err != nil {
		return fmt.Errorf("client.naive.server: %w", err)
	}
	u, err := url.Parse("https://" + n.Server)
	if err != nil || u.Host != n.Server || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("client.naive.server must contain only a hostname or IP and port")
	}
	if err := credentials(n.Binary, n.Username, n.Password); err != nil {
		return err
	}
	if n.CAFile != "" {
		if err := readableFile(n.CAFile); err != nil {
			return err
		}
		pem, err := os.ReadFile(n.CAFile)
		if err != nil {
			return err
		}
		if !x509.NewCertPool().AppendCertsFromPEM(pem) {
			return errors.New("client.naive.ca_file contains no PEM certificates")
		}
	}
	return nil
}

func ValidateServer(c *config.ServerConfig) error {
	n := c.Naive
	if !n.Enabled() {
		return nil
	}
	if c.ForwardsUDP() {
		return errors.New("the initial Naive integration forwards TCP only")
	}
	if c.Transport != config.TCP || len(c.FallbackTransports) != 0 {
		return errors.New("Naive currently supports only transport = tcp without transport fallbacks")
	}
	_, targetPort, err := hostPort(c.BindAddr, true)
	if err != nil {
		return fmt.Errorf("server.bind_addr: %w", err)
	}
	_, listenPort, err := hostPort(n.Listen, false)
	if err != nil {
		return fmt.Errorf("server.naive.listen: %w", err)
	}
	if listenPort == targetPort {
		return errors.New("the public Naive port must differ from the loopback reverse port")
	}
	if err := credentials(n.Binary, n.Username, n.Password); err != nil {
		return err
	}
	for _, path := range []string{n.Certificate, n.Key} {
		if err := readableFile(path); err != nil {
			return err
		}
	}
	pair, err := tls.LoadX509KeyPair(n.Certificate, n.Key)
	if err != nil {
		return fmt.Errorf("Naive TLS certificate/key: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("Naive TLS certificate: %w", err)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return errors.New("Naive TLS certificate is expired or not yet valid")
	}
	return nil
}

func clientJSON(n config.NaiveClientConfig, socksAddr string) ([]byte, error) {
	u := url.URL{Scheme: "https", Host: n.Server, User: url.UserPassword(n.Username, n.Password)}
	return json.Marshal(map[string]any{"listen": "socks://" + socksAddr, "proxy": u.String(), "log": ""})
}

func serverJSON(c *config.ServerConfig) ([]byte, error) {
	n := c.Naive
	host, port, err := hostPort(n.Listen, false)
	if err != nil {
		return nil, err
	}
	target, targetPort, err := hostPort(c.BindAddr, true)
	if err != nil {
		return nil, err
	}
	bits := "/32"
	if net.ParseIP(target).To4() == nil {
		bits = "/128"
	}
	return json.Marshal(map[string]any{
		"log":       map[string]any{"level": "warn"},
		"inbounds":  []any{map[string]any{"type": "naive", "listen": host, "listen_port": port, "network": "tcp", "users": []any{map[string]any{"username": n.Username, "password": n.Password}}, "tls": map[string]any{"enabled": true, "certificate_path": n.Certificate, "key_path": n.Key, "alpn": []string{"h2"}}}},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "tunnel"}},
		"route":     map[string]any{"rules": []any{map[string]any{"ip_cidr": []string{target + bits}, "port": []int{targetPort}, "network": "tcp", "action": "route", "outbound": "tunnel"}, map[string]any{"action": "reject"}}},
	})
}
