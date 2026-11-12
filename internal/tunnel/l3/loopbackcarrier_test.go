package l3

import (
	"context"
	"fmt"
	"net"
)

// The carrier this package's tests run the engine over.
//
// Every production carrier — pck, sni, xdi, spoof — opens a raw socket and
// needs CAP_NET_RAW, which an ordinary `go test` does not have. The engine's
// tests are about the engine: the handshake, replay, error correction, the
// pumps, the MTU probe. They need a datagram path between two tunnels on one
// machine and nothing more, so this is the least that is one — an unconnected
// UDP socket on loopback, with no batching, no offload and no obfuscation.
//
// It used to be the production udp carrier, which v2.0.0 removed. It lives in a
// _test.go file so it cannot be named by a config: testCarriers is empty in
// every build that is not a test.
const carrierLoopback = "loopback"

func init() { testCarriers[carrierLoopback] = openLoopback }

// loopbackCarrier is a plain UDP socket that reports what it costs.
type loopbackCarrier struct {
	*net.UDPConn
	overhead int
}

func (c *loopbackCarrier) Overhead() int       { return c.overhead }
func (c *loopbackCarrier) CarrierName() string { return carrierLoopback }

// loopbackSockBuf is what the removed udp carrier asked for. The rate tests
// push bursts faster than a reader drains a default-sized buffer.
const loopbackSockBuf = 4 << 20

// openLoopback binds the listening side at cfg.Addr, or opens an ephemeral
// socket for the dialling side and resolves the peer it will send to.
func openLoopback(ctx context.Context, cfg Config) (DatagramCarrier, net.Addr, error) {
	if cfg.Mode == ModeDial {
		peer, err := resolveDatagramEndpoint(ctx, cfg.Addr)
		if err != nil {
			return nil, nil, fmt.Errorf("l3: resolving the loopback peer %q: %w", cfg.Addr, err)
		}
		local := &net.UDPAddr{IP: net.IPv4zero}
		if peer.IP != nil && peer.IP.To4() == nil {
			local = &net.UDPAddr{IP: net.IPv6zero}
		}
		c, err := bindLoopback(local)
		return c, peer, err
	}
	addr, err := net.ResolveUDPAddr("udp", cfg.Addr)
	if err != nil {
		return nil, nil, fmt.Errorf("l3: resolving the loopback bind %q: %w", cfg.Addr, err)
	}
	c, err := bindLoopback(addr)
	return c, nil, err
}

func bindLoopback(addr *net.UDPAddr) (DatagramCarrier, error) {
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("l3: binding the loopback carrier: %w", err)
	}
	_ = conn.SetReadBuffer(loopbackSockBuf)
	_ = conn.SetWriteBuffer(loopbackSockBuf)
	overhead := 20 + 8
	if addr.IP != nil && addr.IP.To4() == nil {
		overhead = 40 + 8
	}
	return &loopbackCarrier{UDPConn: conn, overhead: overhead}, nil
}
