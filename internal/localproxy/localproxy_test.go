package localproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/socks"
)

// startEcho is a TCP service that echoes what it receives — the destination
// the proxy connects out to.
func startEcho(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); io.Copy(c, c) }(c)
		}
	}()
	return l.Addr().String()
}

func freePort(t *testing.T) int {
	t.Helper()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitListening(t *testing.T, addr string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("proxy never listened on %s", addr)
}

// The built-in SOCKS5 proxy must carry a CONNECT to the destination, no-auth.
func TestSocks5ProxyNoAuth(t *testing.T) {
	Dir = t.TempDir()
	echo := startEcho(t)
	host, portStr, _ := net.SplitHostPort(echo)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	proxyPort := freePort(t)
	if err := Save(Config{Enabled: true, Type: SOCKS5, Port: proxyPort}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx)
	waitListening(t, fmt.Sprintf("127.0.0.1:%d", proxyPort))

	conn, err := socks.Dial(fmt.Sprintf("127.0.0.1:%d", proxyPort), "", "", host, port)
	if err != nil {
		t.Fatalf("socks dial: %v", err)
	}
	defer conn.Close()

	msg := []byte("through-the-socks5-proxy")
	conn.Write(msg)
	got := make([]byte, len(msg))
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != string(msg) {
		t.Fatalf("echo = %q, want %q", got, msg)
	}
}

// The built-in HTTP proxy must tunnel a CONNECT to the destination.
func TestHTTPProxyConnect(t *testing.T) {
	Dir = t.TempDir()
	echo := startEcho(t)

	proxyPort := freePort(t)
	if err := Save(Config{Enabled: true, Type: HTTP, Port: proxyPort}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx)
	proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
	waitListening(t, proxyAddr)

	// Speak the CONNECT method by hand and then use the tunnel as a raw pipe.
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echo, echo)

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil {
		t.Fatalf("read connect response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
	}

	msg := []byte("through-the-http-proxy")
	conn.Write(msg)
	got := make([]byte, len(msg))
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != string(msg) {
		t.Fatalf("echo = %q, want %q", got, msg)
	}
}

// A password-protected proxy must reject the wrong credentials.
func TestSocks5ProxyAuth(t *testing.T) {
	Dir = t.TempDir()
	echo := startEcho(t)
	host, portStr, _ := net.SplitHostPort(echo)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	proxyPort := freePort(t)
	Save(Config{Enabled: true, Type: SOCKS5, Port: proxyPort, Username: "u", Password: "p"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx)
	waitListening(t, fmt.Sprintf("127.0.0.1:%d", proxyPort))

	if _, err := socks.Dial(fmt.Sprintf("127.0.0.1:%d", proxyPort), "u", "wrong", host, port); err == nil {
		t.Fatal("wrong password should have been rejected")
	}
	c, err := socks.Dial(fmt.Sprintf("127.0.0.1:%d", proxyPort), "u", "p", host, port)
	if err != nil {
		t.Fatalf("correct password rejected: %v", err)
	}
	c.Close()
}
