package socks

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// SOCKS5 UDP ASSOCIATE (RFC 1928), the direct-relay form.
//
// The pattern borrows from paqet's associate handler, but where paqet forwarded
// each datagram back through its own tunnel, this node is the exit: it sends the
// datagram straight to its destination and relays the reply. That is what makes
// the built-in SOCKS5 proxy usable for UDP — DNS, QUIC, WireGuard, game traffic
// — not just TCP CONNECT.
//
// A UDP datagram from the client is wrapped:
//
//	+-----+------+------+----------+----------+----------+
//	| RSV | FRAG | ATYP | DST.ADDR | DST.PORT |   DATA   |
//	+-----+------+------+----------+----------+----------+
//	|  2  |  1   |  1   | variable |    2     | variable |
//
// FRAG is not supported (fragmented datagrams are dropped), which every real
// client avoids anyway.

// udpIdle is how long an idle outbound flow is kept before being reclaimed.
const udpIdle = 60 * time.Second

// handleUDPAssociate opens a relay socket, tells the client where to send, and
// relays until the control connection (conn) closes.
func handleUDPAssociate(conn net.Conn) {
	// Bind the relay on the same IP the control connection arrived on (loopback
	// for the built-in proxy), port chosen by the kernel.
	localIP := net.IPv4zero
	if la, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		localIP = la.IP
	}
	relay, err := net.ListenUDP("udp", &net.UDPAddr{IP: localIP, Port: 0})
	if err != nil {
		reply(conn, 0x01)
		return
	}
	defer relay.Close()

	bound := relay.LocalAddr().(*net.UDPAddr)
	// Reply: success + the address the client must send its datagrams to.
	if _, err := conn.Write(udpBindReply(bound)); err != nil {
		return
	}

	// The UDP association lives only as long as its TCP control connection: when
	// the client closes it (or it drops), tear the relay down.
	go func() {
		io.Copy(io.Discard, conn)
		relay.Close()
	}()

	r := &udpRelay{relay: relay, flows: map[string]*udpFlow{}}
	defer r.closeAll()
	r.serve()
}

// udpBindReply builds the SOCKS5 reply naming the relay's bound address.
func udpBindReply(a *net.UDPAddr) []byte {
	out := []byte{ver5, repOK, 0x00}
	if ip4 := a.IP.To4(); ip4 != nil {
		out = append(out, atypIPv4)
		out = append(out, ip4...)
	} else {
		out = append(out, atypIPv6)
		out = append(out, a.IP.To16()...)
	}
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(a.Port))
	return append(out, p[:]...)
}

// udpRelay is one UDP ASSOCIATE session: the client-facing socket plus one
// outbound socket per destination, so replies find their way back.
type udpRelay struct {
	relay  *net.UDPConn
	client *net.UDPAddr // learned from the first datagram

	mu    sync.Mutex
	flows map[string]*udpFlow // keyed by destination
}

type udpFlow struct {
	out    *net.UDPConn
	header []byte // SOCKS5 UDP header for replies from this destination
}

func (r *udpRelay) serve() {
	buf := make([]byte, 64*1024)
	for {
		n, from, err := r.relay.ReadFromUDP(buf)
		if err != nil {
			return
		}
		// The first sender is taken to be the client; datagrams from anyone else
		// are ignored, so the relay cannot be used as an open reflector.
		if r.client == nil {
			r.client = from
		} else if from.Port != r.client.Port || !from.IP.Equal(r.client.IP) {
			continue
		}
		host, port, data, hdr, ok := parseUDPDatagram(buf[:n])
		if !ok {
			continue
		}
		r.forward(net.JoinHostPort(host, strconv.Itoa(port)), data, hdr)
	}
}

// forward sends data to dst, opening (and caching) an outbound socket and a
// reply pump the first time a destination is seen.
func (r *udpRelay) forward(dst string, data, header []byte) {
	r.mu.Lock()
	f := r.flows[dst]
	r.mu.Unlock()

	if f == nil {
		raddr, err := net.ResolveUDPAddr("udp", dst)
		if err != nil {
			return
		}
		out, err := net.DialUDP("udp", nil, raddr)
		if err != nil {
			return
		}
		f = &udpFlow{out: out, header: header}
		r.mu.Lock()
		r.flows[dst] = f
		r.mu.Unlock()
		go r.pumpReplies(dst, f)
	}

	f.out.SetWriteDeadline(time.Now().Add(5 * time.Second))
	f.out.Write(data)
	f.out.SetWriteDeadline(time.Time{})
}

// pumpReplies reads from an outbound socket and relays each reply back to the
// client, wrapped in the SOCKS5 UDP header naming its source. It ends when the
// destination goes idle.
func (r *udpRelay) pumpReplies(dst string, f *udpFlow) {
	defer func() {
		r.mu.Lock()
		delete(r.flows, dst)
		r.mu.Unlock()
		f.out.Close()
	}()

	buf := make([]byte, 64*1024)
	hlen := copy(buf, f.header)
	for {
		f.out.SetReadDeadline(time.Now().Add(udpIdle))
		n, err := f.out.Read(buf[hlen:])
		if err != nil {
			return
		}
		if r.client == nil {
			return
		}
		if _, err := r.relay.WriteToUDP(buf[:hlen+n], r.client); err != nil {
			return
		}
	}
}

func (r *udpRelay) closeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.flows {
		f.out.Close()
	}
}

// parseUDPDatagram splits a client datagram into destination and payload, and
// returns the SOCKS5 header (RSV/FRAG/ATYP/ADDR/PORT) to prepend on replies.
func parseUDPDatagram(b []byte) (host string, port int, data, header []byte, ok bool) {
	if len(b) < 4 || b[2] != 0x00 { // RSV, and FRAG must be 0 (no fragments)
		return
	}
	atyp := b[3]
	pos := 4
	switch atyp {
	case atypIPv4:
		if len(b) < pos+4+2 {
			return
		}
		host = net.IP(b[pos : pos+4]).String()
		pos += 4
	case atypIPv6:
		if len(b) < pos+16+2 {
			return
		}
		host = net.IP(b[pos : pos+16]).String()
		pos += 16
	case atypHost:
		if len(b) < pos+1 {
			return
		}
		l := int(b[pos])
		pos++
		if len(b) < pos+l+2 {
			return
		}
		host = string(b[pos : pos+l])
		pos += l
	default:
		return
	}
	port = int(binary.BigEndian.Uint16(b[pos : pos+2]))
	pos += 2
	header = append([]byte(nil), b[:pos]...) // everything before DATA
	data = b[pos:]
	return host, port, data, header, true
}
