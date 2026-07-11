package manage

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// httpClientV4 and httpClientV6 force the IP family so we can detect each
// address independently.
func ipClient(network string) *http.Client {
	dialer := &net.Dialer{Timeout: 4 * time.Second}
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
		},
	}
}

func fetchIP(network string) string {
	client := ipClient(network)
	urls := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://api.ip.sb/ip",
		"https://ipinfo.io/ip",
		"https://icanhazip.com",
	}
	for _, url := range urls {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
		resp.Body.Close()
		ip := strings.TrimSpace(string(body))
		if net.ParseIP(ip) != nil {
			return ip
		}
	}
	return ""
}

// PublicIPv4 returns the server's public IPv4 address, or "-" if unavailable.
func PublicIPv4() string {
	if ip := fetchIP("tcp4"); ip != "" {
		return ip
	}
	return "-"
}

// PublicIPv6 returns the server's public IPv6 address, or "-" if unavailable.
func PublicIPv6() string {
	if ip := fetchIP("tcp6"); ip != "" {
		return ip
	}
	return "-"
}

// PortInUse reports whether a TCP port is already bound locally.
func PortInUse(port string) bool {
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return true
	}
	ln.Close()
	return false
}
