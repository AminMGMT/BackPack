package network

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/gorilla/websocket"
)

// A wss client knows the server it reached, not just whatever answered.
//
// Something terminating the TLS on the path could answer the upgrade itself —
// it needs no token for that — and then tell the client which addresses to
// dial for every connection it carried. The server now proves itself in the
// upgrade response, and the client refuses an answer that is wrong, and a
// missing answer from a server that has answered before.
func TestAWSSClientRefusesAServerThatCannotProveItself(t *testing.T) {
	const token = "wss-server-proof-token-0123456789"
	var mode atomic.Value // "right", "none" or "wrong"
	mode.Store("right")
	up := websocket.Upgrader{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var h http.Header
		switch mode.Load().(string) {
		case "right":
			answer, err := WSSServerAnswer(r.TLS, token)
			if err != nil {
				t.Errorf("answer: %v", err)
				return
			}
			h = http.Header{WSSServerProofHeader: {answer}}
		case "wrong":
			h = http.Header{WSSServerProofHeader: {strings.Repeat("0", 64)}}
		}
		c, err := up.Upgrade(w, r, h)
		if err != nil {
			return
		}
		c.Close()
	}))
	srv.EnableHTTP2 = false
	srv.StartTLS()
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")

	dial := func() error {
		c, err := WebSocketDialer(context.Background(), nil, addr, "", "/channel", 3*time.Second, 10*time.Second,
			true, token, config.WSS, false, 1, 0, 0, 0)
		if c != nil {
			c.Close()
		}
		return err
	}

	mode.Store("wrong")
	if err := dial(); err == nil {
		t.Fatal("a wrong answer from the server was accepted")
	}
	mode.Store("right")
	if err := dial(); err != nil {
		t.Fatalf("the genuine server was refused: %v", err)
	}
	mode.Store("none")
	if err := dial(); err == nil {
		t.Fatal("a server that answered before and now does not was accepted")
	}
}

// An older server sends no answer at all; until it has been seen to answer, a
// new client still connects to it, so the server can be upgraded first or last.
func TestAWSSClientStillReachesAnOlderServer(t *testing.T) {
	up := websocket.Upgrader{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err == nil {
			c.Close()
		}
	}))
	srv.EnableHTTP2 = false
	srv.StartTLS()
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")
	c, err := WebSocketDialer(context.Background(), nil, addr, "", "/channel", 3*time.Second, 10*time.Second,
		true, "some-token-0123456789abcdef", config.WSS, false, 1, 0, 0, 0)
	if err != nil {
		t.Fatalf("an older server was refused: %v", err)
	}
	c.Close()
}

func TestWebSocketUpgradeStopsWithItsAttempt(t *testing.T) {
	for _, mode := range []config.TransportType{config.WS, config.WSMUX, config.WSS, config.WSSMUX} {
		for _, cancellation := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/cancel=%t", mode, cancellation), func(t *testing.T) {
				requested := make(chan struct{})
				release := make(chan struct{})
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					close(requested)
					<-release
					http.Error(w, "released", http.StatusServiceUnavailable)
				}))
				if mode == config.WSS || mode == config.WSSMUX {
					srv.StartTLS()
				} else {
					srv.Start()
				}
				defer srv.Close()
				defer unblock()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				timeout := 150 * time.Millisecond
				if cancellation {
					timeout = 5 * time.Second
				}
				done := make(chan error, 1)
				go func() {
					conn, err := WebSocketDialer(ctx, nil, srv.Listener.Addr().String(), "", "/channel", timeout,
						time.Second, true, "upgrade-cancellation", mode, true, 1, 0, 0, 0)
					if conn != nil {
						conn.Close()
					}
					done <- err
				}()
				select {
				case <-requested:
				case <-time.After(2 * time.Second):
					t.Fatal("HTTP upgrade request never arrived")
				}
				if cancellation {
					cancel()
				}
				select {
				case err := <-done:
					want := context.DeadlineExceeded
					if cancellation {
						want = context.Canceled
					}
					if !errors.Is(err, want) {
						t.Fatalf("upgrade returned %v, want %v", err, want)
					}
				case <-time.After(500 * time.Millisecond):
					unblock()
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						t.Fatal("released upgrade did not finish")
					}
					t.Fatal("HTTP upgrade ignored attempt cancellation or dial timeout")
				}
			})
		}
	}
}

func TestSuccessfulWebSocketOutlivesItsSetupContext(t *testing.T) {
	for _, mode := range []config.TransportType{config.WS, config.WSS} {
		t.Run(string(mode), func(t *testing.T) {
			up := websocket.Upgrader{}
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := up.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				kind, payload, err := conn.ReadMessage()
				if err == nil {
					_ = conn.WriteMessage(kind, payload)
				}
			}))
			if mode == config.WSS {
				srv.StartTLS()
			} else {
				srv.Start()
			}
			defer srv.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conn, err := WebSocketDialer(ctx, nil, srv.Listener.Addr().String(), "", "/channel", 150*time.Millisecond,
				time.Second, true, "setup-deadline-cleared", mode, true, 1, 0, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			cancel()
			time.Sleep(180 * time.Millisecond)
			_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			if err := conn.WriteMessage(websocket.BinaryMessage, []byte("still usable")); err != nil {
				t.Fatal(err)
			}
			_, payload, err := conn.ReadMessage()
			if err != nil || string(payload) != "still usable" {
				t.Fatalf("post-setup echo %q: %v", payload, err)
			}
		})
	}
}
