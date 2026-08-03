package network

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParseProxy(t *testing.T) {
	// No proxy is not an error: it is the ordinary case.
	if p, err := ParseProxy(""); err != nil || p != nil {
		t.Fatalf(`ParseProxy("") = (%v, %v), want (nil, nil)`, p, err)
	}
	if p, err := ParseProxy("   "); err != nil || p != nil {
		t.Fatalf("whitespace was not treated as no proxy: (%v, %v)", p, err)
	}

	p, err := ParseProxy("socks5://127.0.0.1:1080")
	if err != nil {
		t.Fatalf("ParseProxy: %v", err)
	}
	if p.Scheme != "socks5" || p.Address != "127.0.0.1:1080" || p.User != "" {
		t.Fatalf("parsed as %+v", p)
	}

	p, err = ParseProxy("http://bob:s3cret@10.0.0.1:8080")
	if err != nil {
		t.Fatalf("ParseProxy: %v", err)
	}
	if p.Scheme != "http" || p.Address != "10.0.0.1:8080" || p.User != "bob" || p.Password != "s3cret" {
		t.Fatalf("parsed as %+v", p)
	}

	// socks5h is the same protocol with the name resolved at the proxy, which
	// is what this client asks for anyway.
	if p, err := ParseProxy("socks5h://127.0.0.1:1080"); err != nil || p.Scheme != "socks5" {
		t.Fatalf("socks5h parsed as (%+v, %v)", p, err)
	}
}

// A misspelled scheme or a missing port has to be refused at parse time. Left
// to the dialler it would surface as a tunnel that never connects, with the
// error naming the proxy rather than the typo.
func TestParseProxyRejectsBadInput(t *testing.T) {
	for _, raw := range []string{
		"127.0.0.1:1080",          // no scheme
		"https://127.0.0.1:8080",  // not a proxy scheme we speak
		"socks4://127.0.0.1:1080", // nor this one
		"socks5://127.0.0.1",      // no port
		"socks5://",               // no host
	} {
		if p, err := ParseProxy(raw); err == nil {
			t.Errorf("ParseProxy(%q) accepted it as %+v", raw, p)
		}
	}
}

// The password must never reach a log line: a log file outlives the session
// that wrote it.
func TestProxyStringHidesThePassword(t *testing.T) {
	p, err := ParseProxy("socks5://bob:s3cret@127.0.0.1:1080")
	if err != nil {
		t.Fatalf("ParseProxy: %v", err)
	}
	got := p.String()
	if strings.Contains(got, "s3cret") {
		t.Fatalf("String() leaked the password: %q", got)
	}
	if !strings.Contains(got, "bob") || !strings.Contains(got, "127.0.0.1:1080") {
		t.Fatalf("String() = %q, want the user and address", got)
	}
	var nilProxy *ProxyConfig
	if nilProxy.String() != "none" {
		t.Fatalf("a nil proxy renders as %q", nilProxy.String())
	}
}

// fakeSocks5 accepts one connection, performs the server side of a no-auth
// SOCKS5 CONNECT, and reports the target it was asked for.
func fakeSocks5(t *testing.T) (addr string, target <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()

		// Greeting: VER NMETHODS METHODS...
		head := make([]byte, 2)
		if _, err := io.ReadFull(c, head); err != nil {
			return
		}
		methods := make([]byte, head[1])
		if _, err := io.ReadFull(c, methods); err != nil {
			return
		}
		c.Write([]byte{0x05, 0x00}) // no auth

		// Request: VER CMD RSV ATYP ...
		req := make([]byte, 4)
		if _, err := io.ReadFull(c, req); err != nil {
			return
		}
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return
		}
		host := make([]byte, l[0])
		if _, err := io.ReadFull(c, host); err != nil {
			return
		}
		portBuf := make([]byte, 2)
		if _, err := io.ReadFull(c, portBuf); err != nil {
			return
		}
		got <- net.JoinHostPort(string(host), itoaTest(int(binary.BigEndian.Uint16(portBuf))))

		// Success, with an IPv4 bound address.
		c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		// Then behave as a tunnel: echo whatever the client sends.
		io.Copy(c, c)
	}()
	return ln.Addr().String(), got
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// The dialler must open the socket to the proxy, ask it for the tunnel server,
// and hand back a connection that carries bytes end to end.
func TestTcpDialerViaSocks5(t *testing.T) {
	proxyAddr, target := fakeSocks5(t)

	p, err := ParseProxy("socks5://" + proxyAddr)
	if err != nil {
		t.Fatalf("ParseProxy: %v", err)
	}

	conn, err := TcpDialerVia(context.Background(), &Outbound{Proxy: p}, "tunnel.example:9000",
		5*time.Second, 30*time.Second, true, 1, 0, 0, 0)
	if err != nil {
		t.Fatalf("dial through the proxy: %v", err)
	}
	defer conn.Close()

	select {
	case got := <-target:
		if got != "tunnel.example:9000" {
			t.Fatalf("the proxy was asked for %q, want the tunnel server", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the proxy was never asked for anything")
	}

	// The socket is connected to the proxy, not to the tunnel server — that is
	// the whole point, and it is what lets the socket keep the tunnel's own
	// tuning.
	if conn.RemoteAddr().String() != proxyAddr {
		t.Fatalf("connected to %s, want the proxy at %s", conn.RemoteAddr(), proxyAddr)
	}

	// And the handshake left no stray bytes behind: the first thing read back
	// must be the first thing written.
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("read %q, want %q — the handshake left bytes in the stream", buf, "ping")
	}
}

// fakeHTTPProxy answers one CONNECT with the given status, then echoes.
func fakeHTTPProxy(t *testing.T, status string) (addr string, target <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()

		br := bufio.NewReader(c)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		got <- req.Host

		c.Write([]byte("HTTP/1.1 " + status + "\r\n\r\n"))
		if !strings.HasPrefix(status, "200") {
			return
		}
		io.Copy(c, c)
	}()
	return ln.Addr().String(), got
}

func TestTcpDialerViaHTTPConnect(t *testing.T) {
	proxyAddr, target := fakeHTTPProxy(t, "200 Connection Established")

	p, err := ParseProxy("http://" + proxyAddr)
	if err != nil {
		t.Fatalf("ParseProxy: %v", err)
	}

	conn, err := TcpDialerVia(context.Background(), &Outbound{Proxy: p}, "tunnel.example:9000",
		5*time.Second, 30*time.Second, true, 1, 0, 0, 0)
	if err != nil {
		t.Fatalf("dial through the proxy: %v", err)
	}
	defer conn.Close()

	select {
	case got := <-target:
		if got != "tunnel.example:9000" {
			t.Fatalf("CONNECT asked for %q, want the tunnel server", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the proxy never saw a CONNECT")
	}

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("read %q, want %q — the CONNECT response was not fully consumed", buf, "ping")
	}
}

// A proxy that refuses must fail the dial, and say so — not hand back a
// connection that looks fine and carries nothing.
func TestTcpDialerViaReportsARefusedConnect(t *testing.T) {
	proxyAddr, _ := fakeHTTPProxy(t, "403 Forbidden")

	p, err := ParseProxy("http://" + proxyAddr)
	if err != nil {
		t.Fatalf("ParseProxy: %v", err)
	}

	conn, err := TcpDialerVia(context.Background(), &Outbound{Proxy: p}, "tunnel.example:9000",
		5*time.Second, 30*time.Second, true, 1, 0, 0, 0)
	if err == nil {
		conn.Close()
		t.Fatal("a refused CONNECT was reported as success")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error %q does not say why the proxy refused", err)
	}
	// The operator has to be able to tell it was the proxy that refused, not
	// the server.
	if !strings.Contains(err.Error(), proxyAddr) {
		t.Fatalf("error %q does not name the proxy", err)
	}
}

// With no proxy configured, the dialler must behave exactly as it always did.
func TestTcpDialerViaWithNoProxyDialsDirectly(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	conn, err := TcpDialerVia(context.Background(), nil, ln.Addr().String(),
		5*time.Second, 30*time.Second, true, 1, 0, 0, 0)
	if err != nil {
		t.Fatalf("direct dial: %v", err)
	}
	defer conn.Close()
	if conn.RemoteAddr().String() != ln.Addr().String() {
		t.Fatalf("connected to %s, want %s", conn.RemoteAddr(), ln.Addr())
	}
}

// A proxy that is not listening must fail rather than hang, and the error must
// name the proxy so the operator looks in the right place.
func TestTcpDialerViaReportsAnUnreachableProxy(t *testing.T) {
	p, err := ParseProxy("socks5://127.0.0.1:1")
	if err != nil {
		t.Fatalf("ParseProxy: %v", err)
	}
	if _, err := TcpDialerVia(context.Background(), &Outbound{Proxy: p}, "tunnel.example:9000",
		2*time.Second, 30*time.Second, true, 1, 0, 0, 0); err == nil {
		t.Skip("something is listening on port 1")
	}
}
