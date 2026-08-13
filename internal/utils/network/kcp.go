package network

import (
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/xtaci/kcp-go/v5"
	"golang.org/x/crypto/pbkdf2"
)

// noopCloser is what the plain-UDP path returns for its carrier closer: there,
// kcp-go opened the socket and closes it itself, so the caller has nothing of
// its own to close. Returning a real closer either way lets the caller close it
// unconditionally without knowing which carrier it got.
type noopCloser struct{}

func (noopCloser) Close() error { return nil }

// ownedKCPSession wraps a PacketConn we built (spoof, xdi or pck) in a KCP
// session that OWNS the socket, so closing the session closes the socket.
//
// This is the whole of the "address already in use" bug. kcp-go's NewConn2
// deliberately does not take ownership of a caller-provided conn — it expects
// the caller to close it — and nothing did. So on every restart, when a new
// control channel arrived and the tunnel rebuilt itself, the previous run's raw
// receive socket stayed bound to its port; two seconds later the new run tried
// to bind the same port and the listener died with a fatal bind error. The
// plain UDP transport never hit it because it dials through DialWithOptions,
// which opens and owns the socket itself.
//
// NewConn4 is NewConn2 with the ownConn flag exposed. Set true, session.Close()
// closes our socket, which is what every close path in the client already does.
func ownedKCPSession(raddr net.Addr, block kcp.BlockCrypt, dataShards, parityShards int, conn net.PacketConn) (*kcp.UDPSession, error) {
	var convid uint32
	if err := binary.Read(crand.Reader, binary.LittleEndian, &convid); err != nil {
		conn.Close()
		return nil, err
	}
	return kcp.NewConn4(convid, raddr, block, dataShards, parityShards, true, conn)
}

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
	// Spoof, when set, carries the KCP session inside raw IPv4 packets whose
	// source is forged — the "IP Spoofing" transport. Like UseICMP it swaps only
	// the packet layer; everything above is unchanged. See spoofconn_linux.go.
	Spoof *SpoofCarrier
	// Pck, when set, carries the KCP session inside TCP segments this process
	// builds and reads through a packet socket — the "TCP + PCK" transport.
	// Again only the packet layer differs. Unlike Spoof it forges no address:
	// the source is this machine's real one, so the server learns each client
	// from the wire exactly as a UDP socket would. See pckconn_linux.go.
	Pck *PcapCarrier
	// Logf, when set, receives one-line startup notes: the effective MTU and FEC
	// on both ends, and — for the pck carrier — the egress it discovered and
	// whether the kernel-RST guard installed. It exists because a KCP tunnel that
	// never connects is otherwise silent: FEC is not negotiated, so a shard
	// mismatch between the two ends drops every packet with nothing in the log,
	// and the pck carrier's interface/next-hop/guard decisions were computed and
	// then thrown away. Nil disables the notes. It is only ever called at
	// startup, never on the data path.
	Logf func(format string, args ...any)
}

// pckDiagnoser is implemented by the pck carrier alone, to surface in the
// startup log what it discovered about the egress path and whether the
// kernel-RST guard installed. The type assertion against it in KCPListen and
// KCPDial simply does nothing for every other carrier.
type pckDiagnoser interface{ PckDiag() string }

// logSettings emits the effective session parameters, so a mismatch between the
// two ends — which never negotiates and so fails silently — is visible in the
// log of each. Safe to call with a nil Logf.
func (s KCPSettings) logSettings(role string) {
	if s.Logf == nil {
		return
	}
	fec := "off"
	if s.DataShards > 0 && s.ParityShards > 0 {
		fec = fmt.Sprintf("%d:%d", s.DataShards, s.ParityShards)
	}
	s.Logf("KCP %s parameters: MTU=%d (effective %d) FEC=%s sndwnd=%d rcvwnd=%d — both ends MUST match MTU and FEC or the tunnel never connects",
		role, s.MTU, s.effectiveMTU(), fec, s.SndWnd, s.RcvWnd)
}

// SpoofCarrier is the IP-spoofing carrier's tuning, passed down to the raw
// socket. Nil in KCPSettings means the session rides on UDP (or ICMP) instead.
//
// Uplink is the client→server profile, Downlink the server→client one; for a
// symmetric tunnel they are equal. The carrier's own constructor picks which is
// its send and which its receive from the side it is on.
type SpoofCarrier struct {
	Uplink     SpoofProfile
	Downlink   SpoofProfile
	SrcIP      string   // forged source address, empty to keep the real one
	SrcPool    []string // forged sources to rotate through; SrcIP is a member
	PeerIP     string   // peer's real IPv4; required on the server, derived on the client
	Interface  string   // egress device to pin the raw socket to, empty for none
	SockBuf    int      // SO_SNDBUF/SO_RCVBUF for the carrier's sockets, 0 = default
	PeerSrcIP  string   // expected forged source of inbound packets, empty = accept any
	ReplySplit bool     // icmp/icmpv6: client sends Echo Request, server sends Echo Reply
	MTU        int      // fragment sends larger than this, 0 = default 1500
	DPI        SpoofDPI // optional obfuscation knobs (ttl/dscp/port/padding/fake-tls)
}

// spoofOpts turns a carrier's public knobs into the internal constructor's
// options for one side of the tunnel.
func (c SpoofCarrier) spoofOpts(token string, realPeer net.IP) spoofConnOpts {
	return spoofConnOpts{
		token:      token,
		uplink:     c.Uplink,
		downlink:   c.Downlink,
		realPeer:   realPeer,
		srcIP:      c.SrcIP,
		srcPool:    c.SrcPool,
		iface:      c.Interface,
		sockBuf:    c.SockBuf,
		replySplit: c.ReplySplit,
		peerSrc:    c.PeerSrcIP,
		mtu:        c.MTU,
		dpi:        c.DPI,
	}
}

// effectiveMTU returns the MTU KCP should use, which is smaller on the carriers
// whose framing eats into the packet: ICMP echo, or the spoof header. Left as
// configured for plain UDP.
func (s KCPSettings) effectiveMTU() int {
	if s.UseICMP {
		return s.MTU - icmpMTUOverhead
	}
	if s.Spoof != nil {
		// Subtract the larger of the two directions' overhead so neither
		// fragments, whichever way a given packet goes.
		over := spoofOverhead(s.Spoof.Uplink)
		if d := spoofOverhead(s.Spoof.Downlink); d > over {
			over = d
		}
		return s.MTU - over
	}
	if s.Pck != nil {
		// The IP and TCP headers plus the timestamp option. The Ethernet header
		// is outside the IP MTU and so is not counted.
		return s.MTU - pckOverhead
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
	// Stream mode, which kcp-go leaves off by default.
	//
	// Off, every Write becomes its own segment (or run of segments) and the last
	// one is left part-empty however few bytes it holds — so a tunnel carrying
	// SMUX, whose frames are a header plus a payload of whatever size the
	// application happened to write, spends a share of every packet on padding it
	// did not need to send. On, a short write is appended to the segment already
	// queued until that segment is full, so the packets that go out are full ones.
	//
	// What rides on this session is a byte stream (SMUX over KCP), not datagrams,
	// so nothing above it can tell the difference — message boundaries were never
	// meaningful here. Measured on a loopback tunnel it is worth 5-7% on every
	// preset, and it costs no latency: SetWriteDelay(false) below still flushes
	// what is queued on the next tick rather than waiting for a segment to fill.
	session.SetStreamMode(true)
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

// KCPListen opens a KCP listener on bindAddr. It yields reliable, ordered
// sessions carried inside UDP datagrams — or, when the settings ask for it,
// inside ICMP echo (xdi), forged raw IP (spoof) or hand-built TCP (pck).
//
// It returns the listener and a Closer for the underlying carrier socket. The
// two are separate because kcp-go does not own a socket handed to it through
// ServeConn: listener.Close() leaves the raw socket bound, so the caller must
// close the returned Closer as well or the next restart cannot bind the port.
// For the plain UDP path the Closer is a no-op — kcp owns that socket.
func KCPListen(bindAddr, token string, s KCPSettings) (*kcp.Listener, io.Closer, error) {
	block, err := kcpCrypt(token)
	if err != nil {
		return nil, nil, err
	}
	s.logSettings("server")

	// Over ICMP the socket is a raw ICMP one shared by every tunnel on the
	// host, not a UDP socket bound to bindAddr — the bind address's port is
	// meaningless here, and the session tag is what separates tunnels. KCP is
	// handed that socket and does not know the difference.
	if s.UseICMP {
		conn, err := newICMPServerConn(token)
		if err != nil {
			return nil, nil, err
		}
		listener, err := kcp.ServeConn(block, s.DataShards, s.ParityShards, conn)
		if err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("xdi: failed to start the KCP listener: %w", err)
		}
		return listener, conn, nil
	}

	// Over the spoof carrier the send side is a raw IPv4 socket and the receive
	// side an ordinary UDP one; KCP is handed the pair as a single PacketConn and
	// does not know the difference. The server must be told the client's real
	// address, because the forged packets cannot reveal it.
	if s.Spoof != nil {
		peer := net.ParseIP(s.Spoof.PeerIP)
		if peer == nil {
			return nil, nil, fmt.Errorf("spoof: spoof_peer_ip must be set to the client's real IPv4 address on the server")
		}
		conn, err := newSpoofServerConn(s.Spoof.spoofOpts(token, peer))
		if err != nil {
			return nil, nil, err
		}
		listener, err := kcp.ServeConn(block, s.DataShards, s.ParityShards, conn)
		if err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("spoof: failed to start the KCP listener: %w", err)
		}
		return listener, conn, nil
	}

	// The pck carrier is a PacketConn like the others, so KCP is handed it and
	// serves ordinary sessions over it. The bind address supplies the port the
	// tunnel's segments are addressed to; no socket is bound to it, because the
	// packets are read off the wire rather than delivered by the kernel.
	if s.Pck != nil {
		carrier := *s.Pck
		carrier.Token = token
		if carrier.Port == 0 {
			carrier.Port = portOf(bindAddr)
		}
		if carrier.Port == 0 {
			return nil, nil, fmt.Errorf("pck: the tunnel port could not be read from %q", bindAddr)
		}
		conn, err := newPckConn(true, carrier.Port, carrier)
		if err != nil {
			return nil, nil, err
		}
		if d, ok := conn.(pckDiagnoser); ok && s.Logf != nil {
			s.Logf("%s", d.PckDiag())
		}
		listener, err := kcp.ServeConn(block, s.DataShards, s.ParityShards, conn)
		if err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("pck: failed to start the KCP listener: %w", err)
		}
		return listener, conn, nil
	}

	listener, err := kcp.ListenWithOptions(bindAddr, block, s.DataShards, s.ParityShards)
	if err != nil {
		return nil, nil, fmt.Errorf("kcp: failed to listen on %s: %w", bindAddr, err)
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
	return listener, noopCloser{}, nil
}

// KCPDial opens a KCP session to remoteAddr with the tuning applied, over UDP
// or — when the settings ask for it — over ICMP echo (the xdi transport).
func KCPDial(remoteAddr, token string, s KCPSettings) (*kcp.UDPSession, error) {
	block, err := kcpCrypt(token)
	if err != nil {
		return nil, err
	}
	s.logSettings("client")

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
		session, err = ownedKCPSession(ipAddr, block, s.DataShards, s.ParityShards, conn)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("xdi: failed to open the KCP session: %w", err)
		}
	} else if s.Spoof != nil {
		// The real destination every packet is routed to is the server: its
		// configured real address if given, otherwise the host of remoteAddr.
		ipAddr, err := hostToIPAddr(remoteAddr)
		if err != nil {
			return nil, err
		}
		peer := ipAddr.IP
		if s.Spoof.PeerIP != "" {
			if p := net.ParseIP(s.Spoof.PeerIP); p != nil {
				peer = p
			}
		}
		conn, err := newSpoofClientConn(s.Spoof.spoofOpts(token, peer))
		if err != nil {
			return nil, err
		}
		session, err = ownedKCPSession(&net.IPAddr{IP: peer}, block, s.DataShards, s.ParityShards, conn)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("spoof: failed to open the KCP session: %w", err)
		}
	} else if s.Pck != nil {
		// The client addresses its segments to the server's real address and
		// the tunnel port, and sends from an ephemeral port of its own.
		ipAddr, err := hostToIPAddr(remoteAddr)
		if err != nil {
			return nil, err
		}
		carrier := *s.Pck
		carrier.Token = token
		carrier.PeerIP = ipAddr.IP.String()
		if carrier.Port == 0 {
			carrier.Port = portOf(remoteAddr)
		}
		if carrier.Port == 0 {
			return nil, fmt.Errorf("pck: the tunnel port could not be read from %q", remoteAddr)
		}
		conn, err := newPckConn(false, carrier.Port, carrier)
		if err != nil {
			return nil, err
		}
		if d, ok := conn.(pckDiagnoser); ok && s.Logf != nil {
			s.Logf("%s", d.PckDiag())
		}
		session, err = ownedKCPSession(&net.UDPAddr{IP: ipAddr.IP, Port: int(carrier.Port)},
			block, s.DataShards, s.ParityShards, conn)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("pck: failed to open the KCP session: %w", err)
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

// portOf returns the port of a "host:port" address, or 0 when there is none.
// The pck carrier uses it to take the tunnel port straight from the address the
// operator configured, so a capture shows the port they expect.
func portOf(addr string) uint16 {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return uint16(n)
}
