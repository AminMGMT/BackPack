package transport

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"

	"github.com/backpack/backpack/internal/utils/network"
)

// isTunnelRequest reports whether a request is a genuine tunnel connection —
// a websocket upgrade, on a tunnel path, carrying a valid credential. Anything
// else (a browser, a scanner, a probe with the wrong token) is not, and is
// answered with the decoy site instead.
func isTunnelRequest(r *http.Request, token string) bool {
	if !websocket.IsWebSocketUpgrade(r) {
		return false
	}
	if r.URL.Path != "/channel" && !strings.HasPrefix(r.URL.Path, "/tunnel") {
		return false
	}
	return authorizeWSRequest(r, token)
}

// serveDecoy answers a non-tunnel request as an ordinary web server would.
//
// This is what makes a wss tunnel survive scrutiny: on port 443 behind a real
// domain and certificate, the server has to look like a website, not a tunnel.
// A browser or an active probe that hits it — wrong path, no upgrade, wrong
// token — must see a plausible page and a normal 200, not a 401 or a blank
// close that gives the game away. The page is the stock "welcome" placeholder a
// freshly set-up server serves, which is one of the most common and least
// remarkable things on the web; the tunnel itself only answers a websocket
// upgrade on its own path with the right credential.
func serveDecoy(w http.ResponseWriter) {
	w.Header().Set("Server", "nginx")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(decoyPage))
}

const decoyPage = `<!DOCTYPE html>
<html>
<head>
<title>Welcome to nginx!</title>
<style>
body { width: 35em; margin: 0 auto; font-family: Tahoma, Verdana, Arial, sans-serif; }
</style>
</head>
<body>
<h1>Welcome to nginx!</h1>
<p>If you see this page, the nginx web server is successfully installed and
working. Further configuration is required.</p>

<p>For online documentation and support please refer to
<a href="http://nginx.org/">nginx.org</a>.<br/>
Commercial support is available at
<a href="http://nginx.com/">nginx.com</a>.</p>

<p><em>Thank you for using nginx.</em></p>
</body>
</html>
`

// authorizeWSRequest checks the Authorization header on a websocket upgrade.
//
// Over plain ws there is no session to bind to, so the header carries the token
// itself and is compared to the configured one. Over wss the client sends a
// proof bound to the TLS session instead of the token, and the server recomputes
// the expected proof from its own side of that session; a man in the middle that
// terminated the client's TLS holds a different session and cannot produce it.
// Either way the comparison is constant time, and a wss connection whose keying
// material cannot be exported is rejected rather than waved through.
func authorizeWSRequest(r *http.Request, token string) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

	if r.TLS == nil {
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

type LocalAcceptUDPConn struct {
	timeCreated int64
	payload     chan []byte
	remoteAddr  string
	listener    *net.UDPConn
	clientAddr  *net.UDPAddr
	IsCongested bool // for congested tcp connection
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
