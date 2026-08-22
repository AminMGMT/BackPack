package transport

import (
	"crypto/subtle"
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

// listenAddrFor turns the left-hand side of a `local=remote` forwarding
// mapping into an address to listen on. A bare number is shorthand for "this
// port on every interface"; anything else is already a full address and is
// handed back untouched.
//
// Every transport used to inline this, and every one of them wrote the range
// check as `port > 1 && port < 65535`. Ports 1 and 65535 are perfectly valid
// and the config validator accepts them, so `1=127.0.0.1:80` fell through to
// the else branch and was passed to the listener as the literal address "1",
// which fails to resolve. Written once, the bound is stated once.
func listenAddrFor(localPortOrAddr string) string {
	value := strings.TrimSpace(localPortOrAddr)
	if port, err := strconv.Atoi(value); err == nil && port >= 1 && port <= 65535 {
		return ":" + strconv.Itoa(port)
	}
	return value
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
