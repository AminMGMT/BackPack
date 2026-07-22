package e2e

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// PROXY protocol v2 over every transport that advertises support for it.
//
// This is the regression guard for a bug that made KCP unusable whenever the
// real-client-IP option was on: the header builder cast the *outbound* tunnel
// connection to *net.TCPAddr, which is a *net.UDPAddr on the datagram
// transports. The cast failed, WriteProxyProtocol returned an error, and the
// forwarded connection was dropped before a byte moved — the tunnel connected
// and then carried nothing. A TCP-only test never caught it because the cast
// happened to succeed there.

var proxyV2Sig = []byte{0x0d, 0x0a, 0x0d, 0x0a, 0x00, 0x0d, 0x0a, 0x51, 0x55, 0x49, 0x54, 0x0a}

// proxyHeader is the parsed source/destination of one PROXY v2 header.
type proxyHeader struct {
	srcIP, dstIP     net.IP
	srcPort, dstPort uint16
}

// readProxyV2 reads and validates a PROXY protocol v2 header from r, returning
// it along with the reader positioned at the first payload byte.
func readProxyV2(r io.Reader) (proxyHeader, error) {
	var h proxyHeader
	fixed := make([]byte, 16) // 12 sig + 1 ver/cmd + 1 fam/proto + 2 len
	if _, err := io.ReadFull(r, fixed); err != nil {
		return h, fmt.Errorf("read header: %w", err)
	}
	if !bytes.Equal(fixed[:12], proxyV2Sig) {
		return h, fmt.Errorf("bad signature %x", fixed[:12])
	}
	if fixed[12] != 0x21 {
		return h, fmt.Errorf("bad version/command %#x", fixed[12])
	}
	length := binary.BigEndian.Uint16(fixed[14:16])
	addr := make([]byte, length)
	if _, err := io.ReadFull(r, addr); err != nil {
		return h, fmt.Errorf("read addresses: %w", err)
	}
	switch fixed[13] {
	case 0x11: // IPv4 + TCP
		h.srcIP, h.dstIP = net.IP(addr[0:4]), net.IP(addr[4:8])
		h.srcPort = binary.BigEndian.Uint16(addr[8:10])
		h.dstPort = binary.BigEndian.Uint16(addr[10:12])
	case 0x21: // IPv6 + TCP
		h.srcIP, h.dstIP = net.IP(addr[0:16]), net.IP(addr[16:32])
		h.srcPort = binary.BigEndian.Uint16(addr[32:34])
		h.dstPort = binary.BigEndian.Uint16(addr[34:36])
	default:
		return h, fmt.Errorf("bad family/protocol %#x", fixed[13])
	}
	return h, nil
}

// startProxyAwareBackend is an echo backend that first consumes a PROXY v2
// header and reports the first one it parsed. A backend that did NOT expect
// the header would read it as payload — which is exactly how a mis-set tunnel
// breaks — so parsing it here proves the header was both sent and well-formed.
func startProxyAwareBackend(t *testing.T) (*echoBackend, <-chan proxyHeader) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot start the proxy-aware backend: %v", err)
	}
	headers := make(chan proxyHeader, 8)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				h, err := readProxyV2(c)
				if err != nil {
					return // drop; the test times out waiting for a header
				}
				select {
				case headers <- h:
				default:
				}
				io.Copy(c, c) // echo the rest
			}(conn)
		}
	}()
	t.Cleanup(func() { l.Close() })
	return &echoBackend{addr: l.Addr().String(), listener: l}, headers
}

// TestProxyProtocolOverEveryTransport runs the real-client-IP path over each
// transport that supports it and checks that the forwarded connection both
// survives and delivers a valid header naming the real client.
func TestProxyProtocolOverEveryTransport(t *testing.T) {
	// Exactly the transports supportsProxyProtocol() allows.
	for _, transport := range []string{"tcp", "tcpmux", "kcp", "wsmux", "stealth"} {
		t.Run(transport, func(t *testing.T) {
			backend, headers := startProxyAwareBackend(t)
			tun := startTunnel(t, transport, backend, tunnelOptions{ProxyProtocol: true})

			conn, err := net.DialTimeout("tcp", tun.Entry, 5*time.Second)
			if err != nil {
				t.Fatalf("dial entry: %v", err)
			}
			defer conn.Close()

			const msg = "real-client-ip-check"
			if _, err := conn.Write([]byte(msg)); err != nil {
				t.Fatalf("write: %v", err)
			}

			// The header must arrive — before the fix this never came for KCP,
			// because the connection was dropped at the header write.
			select {
			case h := <-headers:
				if h.srcPort == 0 || h.srcIP == nil {
					t.Fatalf("header has no source: %+v", h)
				}
				if !h.srcIP.IsLoopback() {
					t.Fatalf("source IP = %v, want the loopback client", h.srcIP)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("backend never received a PROXY header — the forwarded connection was dropped")
			}

			// And the payload after the header must still echo intact.
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			got := make([]byte, len(msg))
			if _, err := io.ReadFull(conn, got); err != nil {
				t.Fatalf("read echo: %v", err)
			}
			if string(got) != msg {
				t.Fatalf("echo = %q, want %q", got, msg)
			}
		})
	}
}
