package network

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/socks"
)

// Reaching the tunnel server through a local proxy.
//
// Some clients cannot open an arbitrary outbound connection at all. A machine
// on a corporate or campus network may only be allowed out through a proxy; a
// box already running a SOCKS5 listener may have that as its one working route
// out; and on a filtered path a proxy on another host is sometimes the only
// address that still answers. In every one of those cases the tunnel's own
// address is reachable and the tunnel still cannot dial it, because it insists
// on connecting directly.
//
// So the client can be told to go through a proxy, exactly as rathole's client
// can: one URL, either socks5:// or http://, with optional credentials.
//
// Only the connection to the tunnel server goes through it. The dial to the
// local backend deliberately cannot — that traffic never leaves the machine,
// and sending it out through a proxy and back would be both slower and wrong.
// The separation is structural rather than a rule to remember: this file is
// reached only from the tunnel dial paths, and the backend dial calls the plain
// TcpDialer, which has no way to take a proxy.

// ProxyConfig is a parsed proxy URL, or nil when the tunnel dials directly.
type ProxyConfig struct {
	// Scheme is "socks5" or "http".
	Scheme string
	// Address is the proxy's host:port.
	Address string
	// User and Password are the credentials, empty when the proxy takes none.
	User     string
	Password string
}

// ParseProxy turns a configured proxy URL into something dialable. An empty
// string means no proxy, which is not an error.
//
// It is strict about the scheme, because the two it understands behave
// completely differently on the wire and a typo would otherwise surface as a
// tunnel that cannot connect for no stated reason.
func ParseProxy(raw string) (*ProxyConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("proxy: %q is not a URL: %w", raw, err)
	}

	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "socks5", "socks5h":
		// socks5h is the same protocol; the h only says the proxy resolves the
		// name, which is what this client asks for in either case.
		scheme = "socks5"
	case "http":
	default:
		return nil, fmt.Errorf("proxy: unknown scheme %q, expected socks5:// or http://", u.Scheme)
	}

	if u.Host == "" {
		return nil, fmt.Errorf("proxy: %q names no host", raw)
	}
	if _, _, err := net.SplitHostPort(u.Host); err != nil {
		return nil, fmt.Errorf("proxy: %q needs a port, as in socks5://127.0.0.1:1080", raw)
	}

	p := &ProxyConfig{Scheme: scheme, Address: u.Host}
	if u.User != nil {
		p.User = u.User.Username()
		p.Password, _ = u.User.Password()
	}
	return p, nil
}

// String renders the proxy for a log line, with the password removed. A tunnel
// logs the route it is taking, and a password in a log file outlives the
// session that wrote it.
func (p *ProxyConfig) String() string {
	if p == nil {
		return "none"
	}
	if p.User != "" {
		return p.Scheme + "://" + p.User + ":***@" + p.Address
	}
	return p.Scheme + "://" + p.Address
}

// connectThrough completes the proxy handshake on an open connection to the
// proxy, leaving it connected to target.
func connectThrough(conn net.Conn, p *ProxyConfig, target string, timeout time.Duration) error {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("proxy: %q is not a host:port: %w", target, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("proxy: %q has no usable port: %w", target, err)
	}

	switch p.Scheme {
	case "socks5":
		return socks.Negotiate(conn, p.User, p.Password, host, port)
	case "http":
		return httpConnect(conn, p, target, timeout)
	}
	return fmt.Errorf("proxy: unknown scheme %q", p.Scheme)
}

// httpConnect performs an HTTP CONNECT, which is how an ordinary web proxy is
// asked for a raw tunnel to somewhere else.
func httpConnect(conn net.Conn, p *ProxyConfig, target string, timeout time.Duration) error {
	if timeout > 0 {
		if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}
		defer conn.SetDeadline(time.Time{})
	}

	req, err := http.NewRequest(http.MethodConnect, "http://"+target, nil)
	if err != nil {
		return fmt.Errorf("proxy: building the CONNECT request: %w", err)
	}
	// Opaque, so the request line is "CONNECT host:port HTTP/1.1" rather than a
	// full URL — which is what a proxy expects for CONNECT and what some of
	// them reject anything else for.
	req.URL = &url.URL{Opaque: target}
	req.Host = target
	if p.User != "" || p.Password != "" {
		req.SetBasicAuth(p.User, p.Password)
		req.Header.Set("Proxy-Authorization", req.Header.Get("Authorization"))
		req.Header.Del("Authorization")
	}

	if err := req.Write(conn); err != nil {
		return fmt.Errorf("proxy: sending CONNECT: %w", err)
	}

	// The reader is deliberately not kept. A proxy that answers 200 must send
	// nothing further before the tunnelled bytes, so anything buffered past the
	// response would be tunnel data — and dropping the reader would drop it.
	// bufio only reads ahead when asked to, and ReadResponse on a CONNECT with
	// no body stops at the header, so there is nothing held back.
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return fmt.Errorf("proxy: reading the CONNECT response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("proxy: CONNECT to %s refused: %s", target, resp.Status)
	}
	if n := br.Buffered(); n > 0 {
		// Should not happen against a well-behaved proxy, and silently losing
		// the first bytes of the tunnel would look like a corrupt stream.
		return fmt.Errorf("proxy: %d bytes arrived before the tunnel started; refusing to lose them", n)
	}
	return nil
}
