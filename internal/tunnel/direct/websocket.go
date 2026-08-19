package direct

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// The websocket transports.
//
// What these buy over plain tcp is the HTTP upgrade in front of the stream. To
// anything watching, the connection opens as an ordinary web request; to a CDN
// it is a websocket it is willing to proxy, which is what lets a tunnel reach
// a kharej server through an edge network rather than directly.
//
// # Only the handshake is a websocket
//
// Once the upgrade has completed, this takes the connection out from under
// gorilla and uses the raw stream beneath it. Websocket framing would cost two
// to fourteen bytes per message and buy nothing: smux above already has its
// own framing, and the two ends have already agreed what the connection is
// for. The reverse mux transports do exactly the same thing, for the same
// reason — see their use of NetConn.
//
// So the shape is: HTTP upgrade for camouflage, then the byte stream, then the
// token proof, then smux. What travels is identical to the tcp transport; only
// the way the connection opens differs.
//
// # What TLS is and is not doing here
//
// On wss the stream is inside TLS, which is what makes it look like ordinary
// HTTPS and what a CDN requires. It is not the tunnel's trust anchor: that is
// the token, proved inside, over every transport alike. A self-signed
// certificate is therefore perfectly serviceable, and is what an origin gets
// if no certificate is configured.

// wsPath is where the upgrade is served. It looks like an ordinary endpoint,
// which is the point.
const wsPath = "/tunnel"

// wsHandshakeTimeout bounds the upgrade itself.
const wsHandshakeTimeout = 15 * time.Second

// dialWebSocket opens the edge's side of a ws or wss connection and hands back
// the raw stream underneath it.
func dialWebSocket(ctx context.Context, cfg *Config) (net.Conn, error) {
	scheme := "ws"
	if cfg.Transport == TransportWSS {
		scheme = "wss"
	}
	endpoint := url.URL{Scheme: scheme, Host: cfg.Addr, Path: wsPath}

	dialer := &websocket.Dialer{
		HandshakeTimeout: wsHandshakeTimeout,
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: cfg.DialTimeout}
			conn, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			tuneConn(conn, cfg)
			return conn, nil
		},
	}

	if cfg.Transport == TransportWSS {
		dialer.TLSClientConfig = &tls.Config{
			ServerName: cfg.serverName(),
			// The tunnel authenticates on the token, inside, so a self-signed
			// certificate at the origin is expected and normal. Verification
			// is turned on only when the operator has said which name to
			// expect, which is the case where it can actually mean something.
			InsecureSkipVerify: cfg.ServerName == "",
			MinVersion:         tls.VersionTLS12,
		}
	}

	header := http.Header{}
	// A Host that is not the dialled address is how a CDN is told which origin
	// this belongs to, and how a plausible name reaches an inspecting middlebox.
	if cfg.ServerName != "" {
		header.Set("Host", cfg.ServerName)
	}

	conn, resp, err := dialer.DialContext(ctx, endpoint.String(), header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("direct: websocket upgrade to %s refused with %s: %w",
				cfg.Addr, resp.Status, err)
		}
		return nil, fmt.Errorf("direct: websocket upgrade to %s: %w", cfg.Addr, err)
	}

	// From here the websocket framing is not used; the stream underneath is.
	return conn.NetConn(), nil
}

// serverName is the name to present in SNI, falling back to the host of the
// address being dialled.
func (c *Config) serverName() string {
	if c.ServerName != "" {
		return c.ServerName
	}
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return c.Addr
	}
	return host
}

// wsListener accepts websocket upgrades and presents them as ordinary accepted
// connections, so the origin's session loop does not have to know which
// transport it is serving.
type wsListener struct {
	server   *http.Server
	listener net.Listener
	accepted chan net.Conn

	closeOnce sync.Once
	closed    chan struct{}
}

// listenWebSocket binds the origin's side of a ws or wss transport.
func listenWebSocket(cfg *Config, log *logrus.Logger) (*wsListener, error) {
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, err
	}

	if cfg.Transport == TransportWSS {
		tlsConfig, err := originTLSConfig(cfg, log)
		if err != nil {
			listener.Close()
			return nil, err
		}
		listener = tls.NewListener(listener, tlsConfig)
	}

	l := &wsListener{
		listener: listener,
		accepted: make(chan net.Conn),
		closed:   make(chan struct{}),
	}

	upgrader := websocket.Upgrader{
		HandshakeTimeout: wsHandshakeTimeout,
		// The tunnel is not a browser API, so the browser's origin rule
		// protects nobody here; the token does that, inside.
		CheckOrigin: func(*http.Request) bool { return true },
	}

	mux := http.NewServeMux()
	mux.HandleFunc(wsPath, func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			// Upgrade has already written a response.
			log.Debugf("direct: websocket upgrade from %s failed: %v", r.RemoteAddr, err)
			return
		}
		raw := conn.NetConn()
		select {
		case l.accepted <- raw:
			// Handed to the session loop, which now owns it. The handler must
			// not return until then, because returning is what would let the
			// http server close the hijacked connection.
			<-l.closed
		case <-l.closed:
			raw.Close()
		}
	})
	// Anything that is not the upgrade path gets what an ordinary server would
	// give it, so a probe finds a web server rather than a tunnel.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	l.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: wsHandshakeTimeout,
	}
	go func() {
		if err := l.server.Serve(listener); err != nil && !isClosed(err) {
			log.Debugf("direct: websocket server stopped: %v", err)
		}
	}()
	return l, nil
}

// Accept returns the next upgraded connection.
func (l *wsListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.accepted:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *wsListener) Addr() net.Addr { return l.listener.Addr() }

func (l *wsListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
		_ = l.server.Close()
	})
	return nil
}

func isClosed(err error) bool {
	return err == http.ErrServerClosed || strings.Contains(err.Error(), "use of closed network connection")
}
