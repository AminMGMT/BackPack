package handlers

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/web"
	"github.com/gorilla/websocket"
)

// Cancellation is the shutdown path a reload and a transport restart both take.
// The connection it has to interrupt is the awkward one: nothing is being sent
// on it in either direction, so neither Read will ever return on its own. If
// the handler cannot get out of that, a reloaded tunnel keeps the previous
// generation's forwarded connections open for the life of the process.
func TestTCPRelayCancellationClosesBothEnds(t *testing.T) {
	client, from := tcpPair(t)
	to, backend := tcpPair(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		TCPConnectionHandler(ctx, false, from, to, quietLogger(), &web.Usage{}, 8080, false)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("an idle relay did not return after its context was cancelled")
	}

	for name, conn := range map[string]net.Conn{"client": client, "backend": backend} {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Read(make([]byte, 1)); err == nil {
			t.Errorf("the %s side stayed open after cancellation", name)
		}
	}
}

// The same for a websocket-backed forward: ReadMessage on an idle connection
// blocks exactly as long as an idle TCP Read does.
func TestWSRelayCancellationClosesBothEnds(t *testing.T) {
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
	case <-time.After(5 * time.Second):
		t.Fatal("an idle websocket relay did not return after its context was cancelled")
	}

	wsClient.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := wsClient.ReadMessage(); err == nil {
		t.Error("the websocket side stayed open after cancellation")
	}
	backend.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := backend.Read(make([]byte, 1)); err == nil {
		t.Error("the TCP side stayed open after cancellation")
	}
}

// The caller releases its connection quota when the handler returns, so the
// handler must not return while either copy is still running.
func TestRelayReturnsOnlyAfterBothCopiesStop(t *testing.T) {
	client, from := tcpPair(t)
	to, backend := tcpPair(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		TCPConnectionHandler(ctx, false, from, to, quietLogger(), &web.Usage{}, 8080, false)
	}()

	// Ending one direction must take the whole relay down with it.
	client.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay did not return after one direction ended")
	}

	// Both ends are closed by the time it returns, which is what makes the
	// other copy's Read return rather than linger.
	backend.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := backend.Read(make([]byte, 1)); err == nil {
		t.Error("the far end was still open after the relay returned")
	}
}

// websocketPair returns the two ends of a real websocket connection.
func websocketPair(t *testing.T) (client, server *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	server = <-accepted
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}
