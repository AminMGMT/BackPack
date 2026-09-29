package controlwire

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func wsPair(t *testing.T) (server, client *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		accepted <- c
	}))
	t.Cleanup(srv.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	server = <-accepted
	t.Cleanup(func() { client.Close(); server.Close() })
	return server, client
}

func tcpPair(t *testing.T) (a, b net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); got <- c }()
	a, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b = <-got
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

// Both carriers deliver a signal as the one byte it is.
func TestASignalCrossesEitherCarrierIntact(t *testing.T) {
	a, b := tcpPair(t)
	ws1, ws2 := wsPair(t)
	for name, pair := range map[string][2]Link{
		"stream":    {Net(a), Net(b)},
		"websocket": {WS(ws1), WS(ws2)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := pair[0].Send(7); err != nil {
				t.Fatalf("send: %v", err)
			}
			_ = pair[1].SetReadDeadline(time.Now().Add(2 * time.Second))
			got, err := pair[1].Receive()
			if err != nil || got != 7 {
				t.Fatalf("received %d, %v; want 7", got, err)
			}
		})
	}
}

// A control channel is absent for the whole window between a drop and the
// next handshake, and the heartbeat keeps firing through it: every operation
// on an absent one is an error, never a panic.
func TestAnAbsentChannelIsAnErrorNotAPanic(t *testing.T) {
	for name, l := range map[string]Link{"stream": Net(nil), "websocket": WS(nil)} {
		t.Run(name, func(t *testing.T) {
			if err := l.Send(1); !errors.Is(err, ErrNoChannel) {
				t.Errorf("a send on no channel: %v, want ErrNoChannel", err)
			}
			if _, err := l.Receive(); !errors.Is(err, ErrNoChannel) {
				t.Errorf("a receive on no channel: %v, want ErrNoChannel", err)
			}
			if err := l.SetReadDeadline(time.Now()); !errors.Is(err, ErrNoChannel) {
				t.Errorf("a deadline on no channel: %v, want ErrNoChannel", err)
			}
			l.Close()
		})
	}
}

// A websocket frame that is not a single-byte binary message carries no
// signal; it is reported as such, and the channel stays usable.
func TestAFrameThatIsNotASignalIsSkippable(t *testing.T) {
	srv, cli := wsPair(t)
	if err := cli.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := WS(cli).Send(3); err != nil {
		t.Fatal(err)
	}
	l := WS(srv)
	if _, err := l.Receive(); !errors.Is(err, ErrNoSignal) {
		t.Fatalf("a text frame was read as %v, want ErrNoSignal", err)
	}
	if got, err := l.Receive(); err != nil || got != 3 {
		t.Fatalf("the signal after it = %d, %v", got, err)
	}
}

// The bound a websocket send sets must be cleared afterwards, or the next
// write on that connection — a pool connection's data, say — inherits a
// deadline that has nothing to do with it.
func TestAWebsocketSendLeavesNoDeadlineBehind(t *testing.T) {
	srv, cli := wsPair(t)
	if err := WS(cli).Send(1); err != nil {
		t.Fatal(err)
	}
	// Any deadline left behind would be in the future; force the question by
	// writing with gorilla directly after the send returned.
	if err := cli.WriteMessage(websocket.BinaryMessage, []byte{2}); err != nil {
		t.Fatalf("a plain write after a control send failed: %v", err)
	}
	for _, want := range []byte{1, 2} {
		if _, data, err := srv.ReadMessage(); err != nil || data[0] != want {
			t.Fatalf("got %v, %v; want %d", data, err, want)
		}
	}
}
