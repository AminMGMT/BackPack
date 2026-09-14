package transport

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/backpack/backpack/internal/utils/network"
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

// isTunnelRequest reports whether a request is a genuine tunnel connection —
// a websocket upgrade, on a tunnel path, carrying a valid credential. Anything
// else (a browser, a scanner, a probe with the wrong token) is not, and is
// answered with the decoy site instead.
func isTunnelRequest(r *http.Request, token string, simpleAuth bool) bool {
	if !websocket.IsWebSocketUpgrade(r) {
		return false
	}
	if r.URL.Path != "/channel" && !strings.HasPrefix(r.URL.Path, "/tunnel") {
		return false
	}
	return authorizeWSRequest(r, token, simpleAuth)
}

// authorizeWSRequest checks the Authorization header on a websocket upgrade.
//
// Over plain ws there is no session to bind to, so the header carries the token
// itself and is compared to the configured one. Over wss the client sends a
// proof bound to the TLS session instead of the token, and the server recomputes
// the expected proof from its own side of that session; a man in the middle that
// terminated the client's TLS holds a different session and cannot produce it.
// Either way the comparison is constant time, and a wss connection whose keying
// material cannot be exported is rejected rather than waved through.
//
// simpleAuth turns the binding off and compares the raw token even over TLS.
// That is exactly what the binding exists to prevent — anyone who terminates
// the TLS then sees a token they could replay — so it is off by default. It is
// here for one deployment the binding otherwise makes impossible: a TLS
// terminating reverse proxy in front of the tunnel, NGINX being the usual one.
// There the proxy legitimately holds a different TLS session from the client,
// so a bound proof can never match; the operator who puts a trusted proxy there
// is choosing to trust it with the token, which is the same thing the raw ws
// transport already does.
func authorizeWSRequest(r *http.Request, token string, simpleAuth bool) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

	if r.TLS == nil || simpleAuth {
		return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
	}

	want, err := network.WSSServerProof(r.TLS, token)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

type TunnelChannel struct { // for websocket
	conn *websocket.Conn
	ping chan struct{}
	mu   *sync.Mutex
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
