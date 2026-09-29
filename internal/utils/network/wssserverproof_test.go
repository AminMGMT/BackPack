package network

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
