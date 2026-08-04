package network

import (
	"crypto/sha256"
	"fmt"
	"net"

	"github.com/xtaci/kcp-go/v5"
	"golang.org/x/crypto/pbkdf2"
)

// KCPSettings carries the tuning of a KCP session from the config all the way
// down to the socket. Both the server and the client side fill it from the
// same preset, so the two ends of a tunnel always agree.
type KCPSettings struct {
	MTU          int
	Interval     int
	Resend       int
	NoDelay      int
	NoCongestion int
	SndWnd       int
	RcvWnd       int
	AckNoDelay   bool
	// DataShards/ParityShards configure forward error correction. Both ends
	// MUST use the same values — the parity layer sits below KCP itself, so a
	// mismatch means the peers cannot decode each other's packets at all.
	DataShards   int
	ParityShards int
	// SO_RCVBUF/SO_SNDBUF size the underlying UDP socket. On a high-latency
	// link these matter more than for TCP, because a KCP sender can have a full
	// window in flight with no kernel-side congestion control to pace it.
	SO_RCVBUF int
	SO_SNDBUF int
	// UseICMP carries the KCP session inside ICMP echo instead of UDP — the xdi
	// transport. Everything above the packet layer is identical, so the only
	// thing this changes is which kind of socket the datagrams ride in. See
	// icmpconn_linux.go.
	UseICMP bool
}

// effectiveMTU returns the MTU KCP should use, which is smaller over ICMP
// because the echo header and the tunnel's own framing eat into the packet.
// Left as configured for UDP.
func (s KCPSettings) effectiveMTU() int {
	if s.UseICMP {
		return s.MTU - icmpMTUOverhead
	}
	return s.MTU
}

// kcpCrypt derives the KCP block cipher from the tunnel token. KCP has no
// handshake of its own, so this both encrypts the datagrams and makes the
// tunnel unreadable to anyone who does not already know the token. The key is
// stretched with PBKDF2 so that even a short token yields a usable AES key.
func kcpCrypt(token string) (kcp.BlockCrypt, error) {
	key := pbkdf2.Key([]byte(token), []byte("backpack-kcp-v1"), 100_000, 32, sha256.New)
	block, err := kcp.NewAESBlockCrypt(key)
	if err != nil {
		return nil, fmt.Errorf("kcp: failed to derive cipher: %w", err)
	}
	return block, nil
}

// ApplyKCPSettings pushes the tuning onto a live KCP session. It is called on
// every accepted and dialled session, on both sides.
func ApplyKCPSettings(session *kcp.UDPSession, s KCPSettings) {
	session.SetNoDelay(s.NoDelay, s.Interval, s.Resend, s.NoCongestion)
	session.SetWindowSize(s.SndWnd, s.RcvWnd)
	session.SetMtu(s.effectiveMTU())
	// Write delay off means a small write goes out on the next tick instead of
	// waiting to be batched — the behaviour a tunnel wants.
	session.SetWriteDelay(false)
	session.SetACKNoDelay(s.AckNoDelay)
	// DSCP 46 (Expedited Forwarding) asks routers that honour it to treat the
	// tunnel as low-latency traffic. Networks that ignore it are unaffected.
	session.SetDSCP(46)
	if s.SO_RCVBUF > 0 {
		session.SetReadBuffer(s.SO_RCVBUF)
	}
	if s.SO_SNDBUF > 0 {
		session.SetWriteBuffer(s.SO_SNDBUF)
	}
}

// KCPListen opens a KCP listener on bindAddr. The returned listener yields
// reliable, ordered sessions carried inside UDP datagrams — or, when the
// settings ask for it, inside ICMP echo (the xdi transport).
func KCPListen(bindAddr, token string, s KCPSettings) (*kcp.Listener, error) {
	block, err := kcpCrypt(token)
	if err != nil {
		return nil, err
	}

	// Over ICMP the socket is a raw ICMP one shared by every tunnel on the
	// host, not a UDP socket bound to bindAddr — the bind address's port is
	// meaningless here, and the session tag is what separates tunnels. KCP is
	// handed that socket and does not know the difference.
	if s.UseICMP {
		conn, err := newICMPServerConn(token)
		if err != nil {
			return nil, err
		}
		listener, err := kcp.ServeConn(block, s.DataShards, s.ParityShards, conn)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("xdi: failed to start the KCP listener: %w", err)
		}
		return listener, nil
	}

	listener, err := kcp.ListenWithOptions(bindAddr, block, s.DataShards, s.ParityShards)
	if err != nil {
		return nil, fmt.Errorf("kcp: failed to listen on %s: %w", bindAddr, err)
	}
	if s.SO_RCVBUF > 0 {
		_ = listener.SetReadBuffer(s.SO_RCVBUF)
	}
	if s.SO_SNDBUF > 0 {
		_ = listener.SetWriteBuffer(s.SO_SNDBUF)
	}
	// The tunnel carries its own token handshake, so KCP's own connection
	// filter only needs to reject packets that fail to decrypt.
	_ = listener.SetDSCP(46)
	return listener, nil
}

// KCPDial opens a KCP session to remoteAddr with the tuning applied, over UDP
// or — when the settings ask for it — over ICMP echo (the xdi transport).
func KCPDial(remoteAddr, token string, s KCPSettings) (*kcp.UDPSession, error) {
	block, err := kcpCrypt(token)
	if err != nil {
		return nil, err
	}

	var session *kcp.UDPSession
	if s.UseICMP {
		conn, err := newICMPClientConn(token)
		if err != nil {
			return nil, err
		}
		// ICMP has no ports, so only the host of remoteAddr matters; the port
		// is dropped. NewConn2 takes the address KCP will send every packet to,
		// which is the server's IP.
		ipAddr, err := hostToIPAddr(remoteAddr)
		if err != nil {
			conn.Close()
			return nil, err
		}
		session, err = kcp.NewConn2(ipAddr, block, s.DataShards, s.ParityShards, conn)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("xdi: failed to open the KCP session: %w", err)
		}
	} else {
		session, err = kcp.DialWithOptions(remoteAddr, block, s.DataShards, s.ParityShards)
		if err != nil {
			return nil, fmt.Errorf("kcp: failed to dial %s: %w", remoteAddr, err)
		}
	}

	ApplyKCPSettings(session, s)
	return session, nil
}

// hostToIPAddr turns a "host" or "host:port" string into the *net.IPAddr the
// ICMP socket dials, resolving a name if need be. The port, if any, is dropped:
// ICMP does not have one.
func hostToIPAddr(remoteAddr string) (*net.IPAddr, error) {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	return net.ResolveIPAddr("ip4", host)
}
