package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/gorilla/websocket"
)

func wsPair(t *testing.T, l *Layer) (*WSConn, *WSConn) {
	t.Helper()
	srvC := make(chan *WSConn, 1)
	up := websocket.Upgrader{ReadBufferSize: wsReadBufferSize, WriteBufferSize: wsWriteBufferSize, WriteBufferPool: WSWriteBuffers}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		sc, err := l.WSServer(c)
		if err != nil {
			t.Error(err)
			return
		}
		srvC <- sc
	}))
	t.Cleanup(ts.Close)
	d := websocket.Dialer{ReadBufferSize: wsReadBufferSize, WriteBufferSize: wsWriteBufferSize, WriteBufferPool: WSWriteBuffers}
	raw, _, err := d.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cc, err := l.WSClient(raw)
	if err != nil {
		t.Fatal(err)
	}
	return cc, <-srvC
}

func TestWSConnThroughput(t *testing.T) {
	for _, e := range []config.EncryptionConfig{{}, {Splash: true}, {Encryption: config.EncStealth}} {
		l := NewLayer(e, "tok")
		a, b := wsPair(t, l)
		msg := make([]byte, 64<<10)
		done := make(chan int64)
		go func() {
			var n int64
			for {
				_, r, err := b.NextReader()
				if err != nil {
					done <- n
					return
				}
				buf := make([]byte, 64<<10)
				for {
					k, err := r.Read(buf)
					n += int64(k)
					if err != nil {
						break
					}
				}
			}
		}()
		start := time.Now()
		for time.Since(start) < time.Second {
			if err := a.WriteMessage(websocket.BinaryMessage, msg); err != nil {
				t.Fatal(err)
			}
		}
		a.Close()
		n := <-done
		t.Logf("%s: %.0f MB/s", l.Mode(), float64(n)/time.Since(start).Seconds()/1e6)
	}
}
