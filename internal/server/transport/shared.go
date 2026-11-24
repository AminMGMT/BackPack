package transport

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// controlClaimTimeout is how long the server waits for a freshly accepted peer
// to say who it is before dropping it.
//
// It is the server's half of the number the client calls controlAckTimeout, and
// it is generous for the same reason. The token arrives in the first segment
// after the connection is up, so on a lossy path the wait is not a round trip
// but a round trip plus however many retransmissions it takes to land: TCP's
// first retransmission is a second out, the next three, the next seven. Two
// seconds — which is what the udp transport asked for — covers barely one of
// those, so on the kind of path this tunnel exists for the handshake failed,
// the client backed off and dialled again, and the tunnel flapped without ever
// being disconnected in any way the operator could see.
//
// Waiting longer costs one goroutine per silent peer and nothing else:
// admission runs in the connection's own goroutine precisely so that a peer
// which connects and says nothing never delays the ones behind it.
const controlClaimTimeout = 15 * time.Second

// portListen is one listener a forwarding mapping expands into: the address to
// bind, and the port on its own, which is the target for a mapping that named
// no destination and so forwards each port to itself.
type portListen struct {
	addr string
	port string
}

// expandListenSpec expands the left-hand side of a forwarding mapping into the
// listeners it asks for.
//
// The left-hand side is a port (`443`), a range (`443-450`), or either of those
// with a local address in front of it (`10.0.0.5:443`, `[::1]:443-450`). A bare
// port listens on every interface; naming an address pins that listener to one
// local IP, which is what a multi-homed host needs in order to keep the control
// channel and the exposed ports on separate public addresses — without it both
// land on 0.0.0.0 and the second bind of a shared port number fails with
// "address already in use".
//
// The address was previously understood for a single port only. Every transport
// inlined this parsing and every one of them tested for "-" before it looked for
// a host, so `10.0.0.5:443-450` took the range branch and handed the whole
// string to strconv.Atoi, which fails. Splitting the host off first is what lets
// a range carry one too, and writing it once is what keeps the seven transports
// from disagreeing about it again.
func expandListenSpec(spec string) ([]portListen, error) {
	host, ports := splitListenHost(strings.TrimSpace(spec))

	start, end, err := parsePortRange(ports)
	if err != nil {
		return nil, err
	}

	out := make([]portListen, 0, end-start+1)
	for p := start; p <= end; p++ {
		port := strconv.Itoa(p)
		// Not net.JoinHostPort: an IPv6 host arrives already bracketed, and
		// JoinHostPort would bracket it a second time. An empty host leaves the
		// familiar ":443" wildcard form.
		out = append(out, portListen{addr: host + ":" + port, port: port})
	}
	return out, nil
}

// splitListenHost separates an optional bind address from the port or range
// that follows it.
//
// IPv6 literals are written bracketed, so the separator is the first colon
// after the closing bracket rather than the last colon in the string — an
// unbracketed IPv6 address is indistinguishable from a host and port, and is no
// more accepted here than net.Listen would accept it.
func splitListenHost(spec string) (host, ports string) {
	if i := strings.LastIndex(spec, "]"); i >= 0 {
		if j := strings.Index(spec[i:], ":"); j >= 0 {
			return spec[:i+j], spec[i+j+1:]
		}
		return spec, ""
	}
	if i := strings.LastIndex(spec, ":"); i >= 0 {
		return spec[:i], spec[i+1:]
	}
	return "", spec
}

// parsePortRange reads "443" or "443-450" into an inclusive pair.
func parsePortRange(spec string) (int, int, error) {
	spec = strings.TrimSpace(spec)
	lo, hi, isRange := strings.Cut(spec, "-")

	start, err := parseListenPort(lo)
	if err != nil {
		return 0, 0, err
	}
	if !isRange {
		return start, start, nil
	}

	end, err := parseListenPort(hi)
	if err != nil {
		return 0, 0, err
	}
	if end < start {
		return 0, 0, fmt.Errorf("port range ends before it starts: %s", spec)
	}
	return start, end, nil
}

// parseListenPort parses one port. The bound is inclusive at both ends: 1 and
// 65535 are valid ports and the config validator accepts them, so a mapping
// like `1=127.0.0.1:80` has to survive this. Every transport used to write the
// check as `port > 1 && port < 65535`, which rejected both.
func parseListenPort(s string) (int, error) {
	s = strings.TrimSpace(s)
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("invalid port: %q", s)
	}
	return p, nil
}

type LocalTCPConn struct {
	conn        net.Conn
	remoteAddr  string
	timeCreated int64
}

type LocalUDPConn struct {
	timeCreated int64
	payload     chan []byte
	remoteAddr  string
	listener    *net.UDPConn
	addr        *net.UDPAddr
}

type TunnelUDPConn struct {
	timeCreated int64
	payload     chan []byte
	addr        *net.UDPAddr
	listener    *net.UDPConn
	ping        chan struct{}
	mu          *sync.Mutex //mutex for ping channel
}
