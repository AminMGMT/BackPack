package core

import (
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/sirupsen/logrus"
)

// Backpack: the encryption a stream tunnel may wrap its connections in
// (config.EncryptionConfig). With the mode left at none nothing
// here runs and the wire is the plain one.
//
// The layer goes on every tunnel connection — the control channel and each
// pooled one — directly over the socket, before the plain engine's own handshake, so
// that handshake and everything after it travel inside.

// layerHandshakeTimeout bounds one connection's encryption handshake.
const layerHandshakeTimeout = 10 * time.Second

// layerHandshakes bounds the handshakes a listener runs at once, so a flood
// of connections that never finish cannot hold unbounded goroutines.
const layerHandshakes = 256

// Layer is one tunnel's encryption. A nil *Layer is none: every method passes
// the connection through.
type Layer struct {
	mode   string // an encryption mode; none with splash alone
	splash bool
	token  string
	host   string // the Iran server's host, which scrap's request names
}

// ForHost sets the host scrap's request names (the kharej's remote address)
// and returns l.
func (l *Layer) ForHost(addr string) *Layer {
	if l != nil {
		if h, _, err := net.SplitHostPort(addr); err == nil {
			addr = h
		}
		l.host = addr
	}
	return l
}

// NewLayer returns the tunnel's layer, or nil for none without splash.
func NewLayer(e config.EncryptionConfig, token string) *Layer {
	if !e.Encrypted() && !e.Splash {
		return nil
	}
	return &Layer{mode: e.EncryptionMode(), splash: e.Splash, token: token}
}

// Mode names the layer, for logs.
func (l *Layer) Mode() string {
	switch {
	case l == nil:
		return config.EncNone
	case l.mode == config.EncNone:
		return "splash"
	}
	return l.mode
}

var errUnknownMode = errors.New("unknown encryption mode")

// Client runs the kharej's half of the handshake on c and returns the
// connection to use from then on. On failure c is closed.
func (l *Layer) Client(c net.Conn) (net.Conn, error) {
	if l == nil {
		return c, nil
	}
	var (
		w   net.Conn
		err error
	)
	switch l.mode {
	case config.EncNone:
		w = c
	case config.EncStealth:
		w, err = network.NoiseClientConn(c, l.token, layerHandshakeTimeout)
	case config.EncStealthPlus:
		w, err = network.StealthPlusClientConn(c, l.token, layerHandshakeTimeout)
	case config.EncScrap:
		w, err = scrapClient(c, l.token, l.host)
	default:
		err = errUnknownMode
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return l.wrapSplash(w), nil
}

// Server runs the Iran server's half of the handshake on c. On failure c is
// closed and nothing is sent back: a probe without the token finds a port
// that says nothing.
func (l *Layer) Server(c net.Conn) (net.Conn, error) {
	if l == nil {
		return c, nil
	}
	var (
		w   net.Conn
		err error
	)
	switch l.mode {
	case config.EncNone:
		w = c
	case config.EncStealth:
		w, err = network.NoiseServerConn(c, l.token, layerHandshakeTimeout)
	case config.EncStealthPlus:
		w, err = network.StealthPlusServerConn(c, l.token, layerHandshakeTimeout)
	case config.EncScrap:
		w, err = scrapServer(c, l.token)
	default:
		err = errUnknownMode
	}
	if err != nil {
		if l.mode == config.EncStealthPlus {
			// Where a probe is cut off would say how much it took to be
			// refused; stealth+ reads on for a while it chooses at random.
			go drainAndClose(c)
			return nil, err
		}
		c.Close()
		return nil, err
	}
	return l.wrapSplash(w), nil
}

// drainSlots bounds the refused connections being drained at once; past it a
// refusal closes at once.
var drainSlots = make(chan struct{}, 128)

// drainAndClose reads and discards from a refused connection for a random
// while, or a random number of bytes, before closing it.
func drainAndClose(c net.Conn) {
	select {
	case drainSlots <- struct{}{}:
		defer func() { <-drainSlots }()
	default:
		c.Close()
		return
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(time.Duration(2+rand.IntN(14)) * time.Second))
	_, _ = io.CopyN(io.Discard, c, int64(rand.IntN(16*1024)))
}

// wrapSplash puts splash inside the encryption, when it is on.
func (l *Layer) wrapSplash(c net.Conn) net.Conn {
	if !l.splash {
		return c
	}
	return newSplash(c)
}

// Listener returns ln with the server's half of the handshake run on every
// accepted connection. Handshakes run side by side, so one slow peer does not
// hold the others back; Accept hands over only the ones that completed.
func (l *Layer) Listener(ln net.Listener, logger *logrus.Logger) net.Listener {
	if l == nil {
		return ln
	}
	ll := &layerListener{Listener: ln, ready: make(chan net.Conn), done: make(chan struct{})}
	go ll.run(l, logger)
	return ll
}

type layerListener struct {
	net.Listener
	ready chan net.Conn
	done  chan struct{}
	once  sync.Once
	err   error // why the accept loop ended; set before done is closed
}

func (ll *layerListener) run(l *Layer, logger *logrus.Logger) {
	sem := make(chan struct{}, layerHandshakes)
	for {
		c, err := ll.Listener.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			ll.closeWith(err)
			return
		}
		select {
		case sem <- struct{}{}:
		case <-ll.done:
			c.Close()
			return
		}
		go func() {
			defer func() { <-sem }()
			w, err := l.Server(c)
			if err != nil {
				logger.Debugf("%s handshake failed from %s: %v", l.mode, c.RemoteAddr(), err)
				return
			}
			select {
			case ll.ready <- w:
			case <-ll.done:
				w.Close()
			}
		}()
	}
}

func (ll *layerListener) Accept() (net.Conn, error) {
	select {
	case c := <-ll.ready:
		return c, nil
	case <-ll.done:
		if ll.err != nil {
			return nil, ll.err
		}
		return nil, net.ErrClosed
	}
}

func (ll *layerListener) Close() error { return ll.closeWith(nil) }

// closeWith ends the listener once; why, when set, is what Accept returns
// from then on. It is written before done is closed, so reading it after is
// safe.
func (ll *layerListener) closeWith(why error) error {
	var err error
	ll.once.Do(func() {
		ll.err = why
		close(ll.done)
		err = ll.Listener.Close()
	})
	return err
}

// Underlying unwraps a layered connection to the socket under it (via
// NetConn, as tls.Conn has), for the socket options the engine sets.
func Underlying(c net.Conn) net.Conn {
	for {
		u, ok := c.(interface{ NetConn() net.Conn })
		if !ok {
			return c
		}
		c = u.NetConn()
	}
}

// TCPOf is the TCP socket under c, layered or not.
func TCPOf(c net.Conn) (*net.TCPConn, bool) {
	t, ok := Underlying(c).(*net.TCPConn)
	return t, ok
}

// rawOf is the socket under c for a syscall-level option.
func rawOf(c net.Conn) (syscall.RawConn, bool) {
	sc, ok := Underlying(c).(syscall.Conn)
	if !ok {
		return nil, false
	}
	raw, err := sc.SyscallConn()
	return raw, err == nil
}
