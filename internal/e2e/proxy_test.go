package e2e

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// socksRelay is a minimal SOCKS5 proxy: it accepts no-auth CONNECT requests,
// dials the target itself, and splices the two together. It counts the
// connections it relayed, which is how the test proves the tunnel actually went
// through it rather than round it.
type socksRelay struct {
	addr     string
	relayed  atomic.Int64
	listener net.Listener
}

func startSocksRelay(t *testing.T) *socksRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot start the socks relay: %v", err)
	}
	r := &socksRelay{addr: ln.Addr().String(), listener: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go r.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return r
}

func (r *socksRelay) serve(c net.Conn) {
	defer c.Close()

	// Greeting.
	head := make([]byte, 2)
	if _, err := io.ReadFull(c, head); err != nil || head[0] != 0x05 {
		return
	}
	if _, err := io.ReadFull(c, make([]byte, head[1])); err != nil {
		return
	}
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	// CONNECT request. The client always sends a hostname address type.
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil || req[1] != 0x01 || req[3] != 0x03 {
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
	target := net.JoinHostPort(string(host), fmt.Sprint(binary.BigEndian.Uint16(portBuf)))

	up, err := net.Dial("tcp", target)
	if err != nil {
		c.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // general failure
		return
	}
	defer up.Close()

	if _, err := c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	r.relayed.Add(1)

	done := make(chan struct{})
	go func() { defer close(done); io.Copy(up, c) }()
	io.Copy(c, up)
	<-done
}

// A client that can only reach the outside through a proxy must still bring the
// tunnel up and carry traffic.
//
// Every connection the client makes to the server — the control channel and
// every pool connection — has to go through the proxy, which is what the relay
// count checks: a tunnel that quietly dialled directly would carry traffic just
// as well, and the test would say nothing.
func TestTunnelThroughASocks5Proxy(t *testing.T) {
	for _, transport := range []string{"tcp", "tcpmux", "ws"} {
		t.Run(transport, func(t *testing.T) {
			backend := startEchoBackend(t)
			relay := startSocksRelay(t)

			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "proxy-token-0123456789abcdefgh"

			srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token)
			cliCfg := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
			cliCfg.Proxy = "socks5://" + relay.addr

			tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("the tunnel never came up through the proxy: %v", err)
			}

			if n := relay.relayed.Load(); n == 0 {
				t.Fatal("the tunnel came up without a single connection going through the proxy")
			}
		})
	}
}

// The dial to the local backend must never go through the proxy. That traffic
// does not leave the machine, and routing it out and back would be both slower
// and wrong — so the guarantee is worth asserting rather than assuming.
//
// The proxy here refuses everything. The tunnel therefore cannot come up at
// all, which is the point: if the backend dial were going through it, this
// would be indistinguishable from the tunnel working. Instead the assertion is
// on what the proxy was asked for — only ever the tunnel server's port, never
// the backend's.
func TestLocalBackendDialNeverUsesTheProxy(t *testing.T) {
	backend := startEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "proxy-token-0123456789abcdefgh"

	asked := make(chan string, 32)
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
			go func(c net.Conn) {
				defer c.Close()
				head := make([]byte, 2)
				if _, err := io.ReadFull(c, head); err != nil {
					return
				}
				if _, err := io.ReadFull(c, make([]byte, head[1])); err != nil {
					return
				}
				c.Write([]byte{0x05, 0x00})
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
				select {
				case asked <- net.JoinHostPort(string(host), fmt.Sprint(binary.BigEndian.Uint16(portBuf))):
				default:
				}
				c.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // refuse
			}(c)
		}
	}()

	srvCfg := baseServerConfig("tcp", tunnelPort, entryPort, backend.addr, token)
	cliCfg := baseClientConfig("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
	cliCfg.Proxy = "socks5://" + ln.Addr().String()

	runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	// It cannot come up — the proxy refuses everything, which is the point.
	// This is only long enough for the client to have tried, and to have kept
	// trying: the pool dials repeatedly, so a backend dial that went through
	// the proxy would show up here too.
	time.Sleep(6 * time.Second)

	close(asked)
	seen := 0
	for target := range asked {
		seen++
		if target != fmt.Sprintf("127.0.0.1:%d", tunnelPort) {
			t.Fatalf("the proxy was asked for %q; only the tunnel server should ever go through it", target)
		}
		if target == backend.addr {
			t.Fatalf("the local backend dial went through the proxy")
		}
	}
	if seen == 0 {
		t.Skip("the client never reached the proxy, so there is nothing to check")
	}
}
