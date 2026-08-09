package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/web"
	"github.com/gorilla/websocket"
)

func websocketPair(t *testing.T) (client, server *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
	}))
	t.Cleanup(httpServer.Close)

	var err error
	client, _, err = websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	server = <-accepted
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

func TestWebSocketRelayCancellationClosesBothEnds(t *testing.T) {
	wsClient, wsServer := websocketPair(t)
	tcpRelay, backend := tcpPair(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		WSConnectionHandler(ctx, wsServer, tcpRelay, quietLogger(), &web.Usage{}, 8080, false)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("an idle websocket relay ignored context cancellation")
	}

	wsClient.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := wsClient.ReadMessage(); err == nil {
		t.Error("websocket side stayed open after cancellation")
	}
	backend.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := backend.Read(make([]byte, 1)); err == nil {
		t.Error("TCP side stayed open after cancellation")
	}
}
