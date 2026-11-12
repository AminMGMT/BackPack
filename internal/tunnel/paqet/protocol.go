package paqet

// Derived from paqet (https://github.com/hanselime/paqet), MIT licence; see
// doc.go for the notice. Changed: PHello, status replies, no PTCPF.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
)

// Message types on a stream. The first message on every stream says what the
// stream is for.
const (
	pPing  byte = 0x01
	pPong  byte = 0x02
	pTCPF  byte = 0x03 // paqet's flag negotiation; reserved, never sent
	pTCP   byte = 0x04
	pUDP   byte = 0x05
	pHello byte = 0x06
)

const (
	// protoMagic and protoVersion open every message. paqet's own version is
	// 0x01; this is Backpack's variant (hello, status replies, framed UDP) and
	// is not wire compatible with it.
	protoMagic   byte = 0x50
	protoVersion byte = 0x02

	// engineRevision is what the hello carries beyond the framing version: it
	// moves when the stream semantics do, so a peer that is too old or too new
	// is named instead of misbehaving. Revision 2 frames the data of a TCP
	// stream so that each direction can end on its own (framed.go); revision 1
	// carried it raw and closed both directions at the first end. Revision 3
	// moves the datagrams of a UDP flow that fit one segment out of the stream
	// and onto the datagram lane beside KCP (datagram.go): a revision-2 peer
	// would never read them, so the two are told apart by name instead.
	engineRevision byte = 3

	headerLen  = 5 // magic, version, type, length (2)
	maxHostLen = 253
	maxBodyLen = 4096
)

// Reply status codes, the byte a server answers a TCP or UDP request with.
const (
	statusOK byte = iota
	statusRefused
	statusTimeout
	statusBadTarget
	statusUnreachable
	// statusForbidden: the target is not the server itself and the server's
	// file does not let it dial others. See targets.go.
	statusForbidden
	// statusBusy: the server has as many forwarded connections open as it
	// allows (maxServerStreams).
	statusBusy
)

// statusError is the error a non-zero status becomes on the client.
type statusError byte

func (s statusError) Error() string {
	switch byte(s) {
	case statusRefused:
		return "the other server's target refused the connection"
	case statusTimeout:
		return "the other server's target did not answer in time"
	case statusBadTarget:
		return "the other server could not make sense of the target address"
	case statusUnreachable:
		return "the other server has no route to the target"
	case statusForbidden:
		return "the other server only connects to its own services (127.0.0.1 and its own addresses); " +
			"to let it reach other machines, set pck_remote_targets = true in the other server's tunnel file"
	case statusBusy:
		return "the other server has too many forwarded connections open and refused one more"
	}
	return "the other server could not reach the target (code " + strconv.Itoa(int(s)) + ")"
}

// Addr is a host and port as a stream request names them.
type Addr struct {
	Host string
	Port int
}

func (a Addr) String() string { return net.JoinHostPort(a.Host, strconv.Itoa(a.Port)) }

// ParseAddr splits "host:port".
func ParseAddr(s string) (Addr, error) {
	host, ps, err := net.SplitHostPort(s)
	if err != nil {
		return Addr{}, err
	}
	port, err := strconv.Atoi(ps)
	if err != nil || port < 0 || port > 65535 {
		return Addr{}, fmt.Errorf("invalid port %q", ps)
	}
	return Addr{Host: host, Port: port}, nil
}

// proto is one decoded message.
type proto struct {
	Type    byte
	Addr    Addr // pTCP, pUDP
	Version byte // pHello: the sender's engine revision
}

var (
	errBadMagic = errors.New("paqet: bad magic byte")
	// errStockPaqet is what a request in paqet's own framing is answered with.
	errStockPaqet = errors.New("paqet: the other end speaks stock paqet framing, not Backpack's")
)

func (p *proto) write(w io.Writer) error {
	body := make([]byte, 0, 64)
	switch p.Type {
	case pPing, pPong:
	case pHello:
		body = append(body, p.Version)
	case pTCP, pUDP:
		host := []byte(p.Addr.Host)
		if len(host) > maxHostLen {
			return fmt.Errorf("paqet: host length %d exceeds max %d", len(host), maxHostLen)
		}
		if p.Addr.Port < 0 || p.Addr.Port > 65535 {
			return fmt.Errorf("paqet: port %d out of range", p.Addr.Port)
		}
		body = append(body, byte(len(host)))
		body = append(body, host...)
		body = binary.BigEndian.AppendUint16(body, uint16(p.Addr.Port))
	default:
		return errors.New("paqet: unknown message type")
	}
	buf := make([]byte, 0, headerLen+len(body))
	buf = append(buf, protoMagic, protoVersion, p.Type)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(body)))
	buf = append(buf, body...)
	_, err := w.Write(buf)
	return err
}

func (p *proto) read(r io.Reader) error {
	var hdr [headerLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	if hdr[0] != protoMagic {
		return fmt.Errorf("%w 0x%02x (want 0x%02x)", errBadMagic, hdr[0], protoMagic)
	}
	if hdr[1] != protoVersion {
		if hdr[1] == 0x01 {
			return errStockPaqet
		}
		return fmt.Errorf("paqet: unsupported framing version 0x%02x (this end speaks 0x%02x)", hdr[1], protoVersion)
	}
	*p = proto{Type: hdr[2]}
	n := int(binary.BigEndian.Uint16(hdr[3:]))
	if n > maxBodyLen {
		return fmt.Errorf("paqet: body length %d exceeds max %d", n, maxBodyLen)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return err
	}
	switch p.Type {
	case pPing, pPong:
		return nil
	case pHello:
		if len(body) != 1 {
			return errors.New("paqet: bad hello body")
		}
		p.Version = body[0]
		return nil
	case pTCP, pUDP:
		if len(body) < 3 {
			return errors.New("paqet: truncated address body")
		}
		hl := int(body[0])
		if hl > maxHostLen || 1+hl+2 != len(body) {
			return fmt.Errorf("paqet: bad host length %d", hl)
		}
		p.Addr = Addr{Host: string(body[1 : 1+hl]), Port: int(binary.BigEndian.Uint16(body[1+hl:]))}
		return nil
	}
	return errors.New("paqet: unknown message type")
}

// Datagram framing on a UDP stream: a two-byte length, then the datagram.
// smux preserves frame boundaries in practice but promises only a byte stream,
// and a UDP forwarder that merges or splits datagrams corrupts whatever
// protocol is inside them, so the boundary is written down.

// maxDatagram is the largest datagram a UDP stream carries.
const maxDatagram = 65535

func writeDatagram(w io.Writer, d []byte) error {
	if len(d) > maxDatagram {
		return fmt.Errorf("paqet: datagram of %d bytes does not fit a frame", len(d))
	}
	buf := make([]byte, 2+len(d))
	binary.BigEndian.PutUint16(buf, uint16(len(d)))
	copy(buf[2:], d)
	_, err := w.Write(buf)
	return err
}
