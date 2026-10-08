// Package naive manages optional Naive and Xray helpers under the existing reverse
// tunnel's context. All reverse protocol semantics remain in the TCP engine.
package naive

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
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

// ValidateXray keeps Xray as a restricted carrier of the existing reverse TCP
// engine. It never enables an independent proxy or Xray reverse service.
func ValidateXray(c *config.Config) error {
	if !c.Server.Xray.Enabled() && !c.Client.Xray.Enabled() {
		return nil
	}
	if c.Server.Naive.Enabled() || c.Client.Naive.Enabled() {
		return errors.New("Xray cannot be combined with Naive")
	}
	if c.Direct.Enabled() || c.L3.Enabled() {
		return errors.New("Xray supports only the TCP reverse engine")
	}
	server, client := c.Server.BindAddr != "", c.Client.RemoteAddr != ""
	if server == client || c.Server.Xray.Enabled() != server || c.Client.Xray.Enabled() != client {
		return errors.New("Xray requires exactly one reverse role and helper settings for that role")
	}
	if server {
		return ValidateXrayServer(&c.Server)
	}
	return ValidateXrayClient(&c.Client)
}

func xrayDNSName(name string) bool {
	if name == "" || len(name) > 253 || net.ParseIP(name) != nil {
		return false
	}
	name = strings.TrimSuffix(name, ".")
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func xrayEndpoint(addr string) (string, int, error) {
	host, port, err := hostPort(addr, false)
	if err != nil {
		return "", 0, err
	}
	if net.ParseIP(host) == nil && !xrayDNSName(host) {
		return "", 0, errors.New("expected a valid hostname or IP and port")
	}
	return host, port, nil
}

func xrayIdentity(binary, mode, id, serverName string) error {
	if err := readableFile(binary); err != nil {
		return fmt.Errorf("Xray helper binary: %w", err)
	}
	if mode != "xhttp" && mode != "reality" {
		return errors.New("Xray mode must be xhttp or reality")
	}
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return errors.New("Xray uuid must be a canonical 36-character UUID")
	}
	raw := strings.ReplaceAll(id, "-", "")
	if len(raw) != 32 {
		return errors.New("Xray uuid must be a canonical 36-character UUID")
	}
	if _, err := hex.DecodeString(raw); err != nil {
		return errors.New("Xray uuid must be a canonical 36-character UUID")
	}
	if !xrayDNSName(serverName) {
		return errors.New("Xray server_name must be an explicit valid DNS name")
	}
	return nil
}

func xrayHTTP(path, host string) error {
	decoded, err := url.PathUnescape(path)
	if err != nil || len(decoded) < 2 || decoded[0] != '/' || strings.ContainsAny(decoded, "?#\\ \t\r\n") {
		return errors.New("XHTTP path must be an explicit non-root path without query, fragment or whitespace")
	}
	for _, c := range decoded {
		if c < 0x20 || c == 0x7f {
			return errors.New("XHTTP path contains a control character")
		}
	}
	if host != "" && !xrayDNSName(host) {
		return errors.New("XHTTP host must be a valid DNS name")
	}
	return nil
}

func xrayReality(key, shortID string) error {
	decoded, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != key {
		return errors.New("REALITY key must encode exactly 32 bytes using unpadded base64url")
	}
	if len(shortID) != 16 {
		return errors.New("REALITY short_id must contain exactly 16 hexadecimal characters")
	}
	if _, err := hex.DecodeString(shortID); err != nil {
		return errors.New("REALITY short_id must contain exactly 16 hexadecimal characters")
	}
	return nil
}

func ValidateXrayClient(c *config.ClientConfig) error {
	x := c.Xray
	if !x.Enabled() {
		return nil
	}
	if c.Naive.Enabled() {
		return errors.New("Xray cannot be combined with Naive")
	}
	if c.Transport != config.TCP || len(c.FallbackTransports) != 0 {
		return errors.New("Xray supports only transport = tcp without transport fallbacks")
	}
	if c.Proxy != "" || c.LocalAddr != "" || c.Interface != "" || c.SOMark != 0 || len(c.FallbackAddrs) != 0 || c.HealthFailover || c.LoadBalance {
		return errors.New("Xray cannot be combined with proxy, routing bindings or endpoint failover/balancing")
	}
	if _, _, err := hostPort(c.RemoteAddr, true); err != nil {
		return fmt.Errorf("client.remote_addr: %w", err)
	}
	if _, _, err := xrayEndpoint(x.Server); err != nil {
		return fmt.Errorf("client.xray.server: %w", err)
	}
	if err := xrayIdentity(x.Binary, x.Mode, x.UUID, x.ServerName); err != nil {
		return err
	}
	if x.Mode == "reality" {
		if x.Path != "" || x.Host != "" || x.CAFile != "" {
			return errors.New("REALITY does not accept XHTTP path, host or TLS ca_file")
		}
		return xrayReality(x.PublicKey, x.ShortID)
	}
	if x.PublicKey != "" || x.ShortID != "" {
		return errors.New("XHTTP does not accept REALITY public_key or short_id")
	}
	if err := xrayHTTP(x.Path, x.Host); err != nil {
		return err
	}
	if x.CAFile != "" {
		if err := readableFile(x.CAFile); err != nil {
			return fmt.Errorf("XHTTP ca_file: %w", err)
		}
		pem, err := os.ReadFile(x.CAFile)
		if err != nil {
			return err
		}
		if !x509.NewCertPool().AppendCertsFromPEM(pem) {
			return errors.New("XHTTP ca_file contains no PEM certificates")
		}
	}
	return nil
}

func ValidateXrayServer(c *config.ServerConfig) error {
	x := c.Xray
	if !x.Enabled() {
		return nil
	}
	if c.Naive.Enabled() {
		return errors.New("Xray cannot be combined with Naive")
	}
	if c.Transport != config.TCP || len(c.FallbackTransports) != 0 || c.ForwardsUDP() {
		return errors.New("Xray supports only reverse TCP forwarding without transport fallbacks")
	}
	_, targetPort, err := hostPort(c.BindAddr, true)
	if err != nil {
		return fmt.Errorf("server.bind_addr: %w", err)
	}
	listenHost, listenPort, err := xrayEndpoint(x.Listen)
	if err != nil {
		return fmt.Errorf("server.xray.listen: %w", err)
	}
	if listenPort == targetPort {
		return errors.New("the public Xray port must differ from the loopback reverse port")
	}
	if err := xrayIdentity(x.Binary, x.Mode, x.UUID, x.ServerName); err != nil {
		return err
	}
	if x.Mode == "reality" {
		if x.Path != "" || x.Host != "" || x.Certificate != "" || x.Key != "" {
			return errors.New("REALITY does not accept XHTTP path, host or TLS certificate/key")
		}
		realityHost, realityPort, err := xrayEndpoint(x.Target)
		if err != nil {
			return fmt.Errorf("server.xray.target: %w", err)
		}
		realityIP, listenIP := net.ParseIP(realityHost), net.ParseIP(listenHost)
		localTarget := realityIP != nil && realityIP.IsLoopback() || strings.EqualFold(strings.TrimSuffix(realityHost, "."), "localhost")
		sameListener := strings.EqualFold(realityHost, listenHost) || realityIP != nil && listenIP != nil && realityIP.Equal(listenIP)
		if listenIP != nil && (listenIP.IsUnspecified() || listenIP.IsLoopback()) && localTarget {
			sameListener = true
		}
		if realityPort == listenPort && sameListener || realityPort == targetPort && localTarget {
			return errors.New("REALITY target must differ from the helper and reverse listeners")
		}
		return xrayReality(x.PrivateKey, x.ShortID)
	}
	if x.PrivateKey != "" || x.Target != "" || x.ShortID != "" {
		return errors.New("XHTTP does not accept REALITY private_key, target or short_id")
	}
	if err := xrayHTTP(x.Path, x.Host); err != nil {
		return err
	}
	for _, path := range []string{x.Certificate, x.Key} {
		if err := readableFile(path); err != nil {
			return fmt.Errorf("XHTTP certificate/key: %w", err)
		}
	}
	pair, err := tls.LoadX509KeyPair(x.Certificate, x.Key)
	if err != nil {
		return fmt.Errorf("XHTTP certificate/key: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("XHTTP certificate: %w", err)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return errors.New("XHTTP TLS certificate is expired or not yet valid")
	}
	if err := leaf.VerifyHostname(x.ServerName); err != nil {
		return errors.New("XHTTP certificate does not match server_name")
	}
	return nil
}

func xrayRouting(inbound, addr, outbound string) (map[string]any, error) {
	host, port, err := hostPort(addr, true)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	bits := "/128"
	if ip.To4() != nil {
		bits = "/32"
	}
	return map[string]any{"domainStrategy": "AsIs", "rules": []any{map[string]any{
		"type": "field", "inboundTag": []string{inbound}, "ip": []string{ip.String() + bits},
		"port": strconv.Itoa(port), "network": "tcp", "outboundTag": outbound,
	}}}, nil
}

// XrayClientJSON emits the pinned Xray VLESS contract, permitting only this
// reverse engine's logical remote target through the private SOCKS listener.
func XrayClientJSON(c *config.ClientConfig, socksAddr string) ([]byte, error) {
	if err := ValidateXrayClient(c); err != nil {
		return nil, err
	}
	x := c.Xray
	host, port, err := hostPort(socksAddr, true)
	if err != nil {
		return nil, fmt.Errorf("Xray SOCKS listener: %w", err)
	}
	server, serverPort, err := xrayEndpoint(x.Server)
	if err != nil {
		return nil, err
	}
	user := map[string]any{"id": x.UUID, "encryption": "none"}
	var stream map[string]any
	if x.Mode == "reality" {
		user["flow"] = "xtls-rprx-vision"
		stream = map[string]any{"network": "raw", "security": "reality", "realitySettings": map[string]any{
			"serverName": x.ServerName, "fingerprint": "chrome", "password": x.PublicKey, "shortId": x.ShortID,
		}}
	} else {
		tlsConfig := map[string]any{"serverName": x.ServerName, "alpn": []string{"h2"}, "allowInsecure": false}
		if x.CAFile != "" {
			tlsConfig["certificates"] = []any{map[string]any{"certificateFile": x.CAFile, "usage": "verify"}}
		}
		httpHost := x.Host
		if httpHost == "" {
			httpHost = x.ServerName
		}
		stream = map[string]any{"network": "xhttp", "security": "tls", "tlsSettings": tlsConfig,
			"xhttpSettings": map[string]any{"path": x.Path, "host": httpHost, "mode": "auto"}}
	}
	routing, err := xrayRouting("local", c.RemoteAddr, "carrier")
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{"tag": "local", "listen": host, "port": port, "protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false}}},
		"outbounds": []any{map[string]any{"tag": "block", "protocol": "blackhole"}, map[string]any{
			"tag": "carrier", "protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{
				"address": server, "port": serverPort, "users": []any{user},
			}}}, "streamSettings": stream,
		}}, "routing": routing,
	})
}

// XrayServerJSON denies all destinations except the reverse listener and fixes
// the allowed outbound's actual destination to that same literal loopback.
func XrayServerJSON(c *config.ServerConfig) ([]byte, error) {
	if err := ValidateXrayServer(c); err != nil {
		return nil, err
	}
	x := c.Xray
	host, port, err := xrayEndpoint(x.Listen)
	if err != nil {
		return nil, err
	}
	user := map[string]any{"id": x.UUID}
	var stream map[string]any
	if x.Mode == "reality" {
		user["flow"] = "xtls-rprx-vision"
		stream = map[string]any{"network": "raw", "security": "reality", "realitySettings": map[string]any{
			"show": false, "target": x.Target, "xver": 0, "serverNames": []string{x.ServerName},
			"privateKey": x.PrivateKey, "shortIds": []string{x.ShortID},
		}}
	} else {
		httpHost := x.Host
		if httpHost == "" {
			httpHost = x.ServerName
		}
		stream = map[string]any{"network": "xhttp", "security": "tls", "xhttpSettings": map[string]any{
			"path": x.Path, "host": httpHost, "mode": "auto"}, "tlsSettings": map[string]any{
			"alpn": []string{"h2"}, "certificates": []any{map[string]any{"certificateFile": x.Certificate, "keyFile": x.Key}},
		}}
	}
	routing, err := xrayRouting("carrier", c.BindAddr, "tunnel")
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{"tag": "carrier", "listen": host, "port": port, "protocol": "vless",
			"settings": map[string]any{"clients": []any{user}, "decryption": "none"}, "streamSettings": stream}},
		"outbounds": []any{map[string]any{"tag": "block", "protocol": "blackhole"}, map[string]any{
			"tag": "tunnel", "protocol": "freedom", "settings": map[string]any{"redirect": c.BindAddr},
		}}, "routing": routing,
	})
}
