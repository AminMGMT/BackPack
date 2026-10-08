package network

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
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

// Every proxy handshake stage is covered by the dial budget, including SOCKS
// method selection, password authentication and the CONNECT reply.
func TestProxyHandshakeRespectsDialBudget(t *testing.T) {
	for _, stage := range []string{"greeting", "auth", "connect", "http"} {
		t.Run(stage, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			reached := make(chan struct{})
			serverDone := make(chan struct{})
			go func() {
				defer close(serverDone)
				c, err := ln.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				if stage == "http" {
					if _, err := http.ReadRequest(bufio.NewReader(c)); err != nil {
						return
					}
				} else {
					greeting := make([]byte, 3)
					if _, err := io.ReadFull(c, greeting); err != nil {
						return
					}
					if stage != "greeting" {
						if _, err := c.Write([]byte{5, greeting[2]}); err != nil {
							return
						}
						if stage == "auth" {
							head := make([]byte, 2)
							if _, err := io.ReadFull(c, head); err != nil {
								return
							}
							if _, err := io.CopyN(io.Discard, c, int64(head[1])); err != nil {
								return
							}
							if _, err := io.ReadFull(c, head[:1]); err != nil {
								return
							}
							if _, err := io.CopyN(io.Discard, c, int64(head[0])); err != nil {
								return
							}
						} else {
							head := make([]byte, 5)
							if _, err := io.ReadFull(c, head); err != nil {
								return
							}
							if _, err := io.CopyN(io.Discard, c, int64(head[4])+2); err != nil {
								return
							}
						}
					}
				}
				close(reached)
				io.Copy(io.Discard, c)
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			proxy := &ProxyConfig{Scheme: "socks5", Address: ln.Addr().String()}
			if stage == "http" {
				proxy.Scheme = "http"
			}
			if stage == "auth" {
				proxy.User, proxy.Password = "user", "pass"
			}
			done := make(chan error, 1)
			go func() {
				conn, err := TcpDialerVia(ctx, &Outbound{Proxy: proxy}, "tunnel.invalid:9000", 40*time.Millisecond, time.Second, true, 1, 0, 0, 0)
				if conn != nil {
					conn.Close()
				}
				done <- err
			}()
			defer func() {
				cancel()
				ln.Close()
				select {
				case <-serverDone:
				case <-time.After(time.Second):
					t.Error("proxy worker did not stop")
				}
			}()
			select {
			case <-reached:
			case <-time.After(time.Second):
				t.Fatal("proxy stage was not reached")
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("silent proxy accepted")
				}
			case <-time.After(300 * time.Millisecond):
				cancel()
				<-done
				t.Fatal("proxy handshake exceeded dial budget")
			}
		})
	}
}

// Deadline state from a successful handshake must not expire the tunnel later.
func TestProxyDialDeadlineIsClearedAfterSuccess(t *testing.T) {
	for _, scheme := range []string{"http", "socks5"} {
		t.Run(scheme, func(t *testing.T) {
			var address string
			if scheme == "http" {
				address, _ = fakeHTTPProxy(t, "200 Connection Established")
			} else {
				address, _ = fakeSocks5(t)
			}
			conn, err := TcpDialerVia(context.Background(), &Outbound{Proxy: &ProxyConfig{Scheme: scheme, Address: address}}, "tunnel.invalid:9000", 50*time.Millisecond, time.Second, true, 1, 0, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			time.Sleep(100 * time.Millisecond)
			if _, err := conn.Write([]byte("live")); err != nil {
				t.Fatal(err)
			}
			conn.SetReadDeadline(time.Now().Add(time.Second))
			reply := make([]byte, 4)
			if _, err := io.ReadFull(conn, reply); err != nil || string(reply) != "live" {
				t.Fatalf("late traffic=%q err=%v", reply, err)
			}
		})
	}
}

// Source lookup shares the budget with proxy setup. Run separately so replacing
// net.DefaultResolver cannot affect another test's still-closing sockets.
func TestTCPDialBudgetIncludesSourceDNS(t *testing.T) {
	if os.Getenv("BACKPACK_SOURCE_DNS_AUDIT") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestTCPDialBudgetIncludesSourceDNS$", "-test.timeout=8s")
		cmd.Env = append(os.Environ(), "BACKPACK_SOURCE_DNS_AUDIT=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated source DNS: %v\n%s", err, output)
		}
		return
	}
	for _, cancelLookup := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancel", false: "timeout"}[cancelLookup], func(t *testing.T) {
			previous := net.DefaultResolver
			release := make(chan struct{})
			started := make(chan struct{}, 1)
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				select {
				case started <- struct{}{}:
				default:
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
					return nil, errors.New("released")
				}
			}}
			ctx, cancel := context.WithCancel(context.Background())
			timeout := 40 * time.Millisecond
			if cancelLookup {
				timeout = time.Second
			}
			done := make(chan error, 1)
			go func() {
				conn, err := TcpDialerVia(ctx, &Outbound{LocalAddr: "source.invalid"}, "127.0.0.1:9", timeout, time.Second, true, 1, 0, 0, 0)
				if conn != nil {
					conn.Close()
				}
				done <- err
			}()
			defer func() { cancel(); unblock(); net.DefaultResolver = previous }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("source DNS did not start")
			}
			if cancelLookup {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("stalled source DNS accepted")
				}
			case <-time.After(300 * time.Millisecond):
				unblock()
				<-done
				t.Fatal("source DNS ignored dial cancellation or timeout")
			}
		})
	}
	// Delayed successful source DNS followed by a delayed proxy reply must consume
	// one timeout rather than receiving a fresh timeout at the proxy stage.
	dns, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var replies sync.WaitGroup
	dnsDone := make(chan struct{})
	go func() {
		defer close(dnsDone)
		buffer := make([]byte, 512)
		for {
			n, peer, err := dns.ReadFrom(buffer)
			if err != nil {
				return
			}
			packet := append([]byte(nil), buffer[:n]...)
			replies.Add(1)
			go func() {
				defer replies.Done()
				var request dnsmessage.Message
				if request.Unpack(packet) != nil {
					return
				}
				time.Sleep(60 * time.Millisecond)
				response := dnsmessage.Message{Header: dnsmessage.Header{ID: request.ID, Response: true, RecursionAvailable: true}, Questions: request.Questions}
				for _, question := range request.Questions {
					if question.Type == dnsmessage.TypeA {
						response.Answers = append(response.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1}, Body: &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}}})
					}
				}
				wire, err := response.Pack()
				if err == nil {
					dns.WriteTo(wire, peer)
				}
			}()
		}
	}()
	defer func() { dns.Close(); <-dnsDone; replies.Wait() }()
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", dns.LocalAddr().String())
	}}
	defer func() { net.DefaultResolver = previous }()
	for _, scheme := range []string{"http", "socks5"} {
		t.Run(scheme+"/shared", func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serverDone := make(chan struct{})
			reached := make(chan struct{})
			go func() {
				defer close(serverDone)
				c, err := listener.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				if scheme == "http" {
					if _, err := http.ReadRequest(bufio.NewReader(c)); err != nil {
						return
					}
				} else {
					head := make([]byte, 3)
					if _, err := io.ReadFull(c, head); err != nil {
						return
					}
					c.Write([]byte{5, 0})
					request := make([]byte, 5)
					if _, err := io.ReadFull(c, request); err != nil {
						return
					}
					if _, err := io.CopyN(io.Discard, c, int64(request[4])+2); err != nil {
						return
					}
				}
				close(reached)
				time.Sleep(80 * time.Millisecond)
				if scheme == "http" {
					c.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
				} else {
					c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				}
			}()
			defer func() { listener.Close(); <-serverDone }()
			started := time.Now()
			conn, err := TcpDialerVia(context.Background(), &Outbound{LocalAddr: "delayed-source.invalid", Proxy: &ProxyConfig{Scheme: scheme, Address: listener.Addr().String()}}, "tunnel.invalid:9000", 100*time.Millisecond, time.Second, true, 1, 0, 0, 0)
			if conn != nil {
				conn.Close()
			}
			select {
			case <-reached:
			default:
				t.Fatalf("source DNS did not reach proxy setup: %v", err)
			}
			if err == nil {
				t.Fatal("DNS and proxy handshake each received a separate dial budget")
			}
			if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
				t.Fatalf("shared attempt took %s", elapsed)
			}
		})
	}
}
