package transport

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/utils/network"
)

// freeUDPAddr takes a loopback UDP port the kernel is not using.
func freeUDPAddr(t *testing.T) string {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().String()
}

// A QUIC peer that has not authenticated cannot hold the server's resources.
//
// Anything that can reach the port can complete QUIC's own handshake — it
// proves nothing about the tunnel token — and each stream it opens used to get
// a goroutine that waited fifteen seconds for an announcement, up to 65,536 of
// them per connection. A connection that has not yet presented the token now
// gets a handful of streams in flight, and one that asks for more than that
// before proving anything is closed.
func TestAnUnauthenticatedQUICPeerCannotOpenStreamsWithoutLimit(t *testing.T) {
	addr := freeUDPAddr(t)
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	srv := NewQuicServer(parent, &QuicConfig{
		BindAddr: addr, Token: "a-long-enough-token", ChannelSize: 16,
		Heartbeat: time.Second, KeepAlive: 10 * time.Second,
	}, quietLogger())
	go srv.Start()

	var conn interface {
		Context() context.Context
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := network.QUICDial(context.Background(), addr, network.QUICSettings{MaxIdleTimeout: 30 * time.Second})
		if err == nil {
			conn = c
			defer c.CloseWithError(0, "")
			// Streams that say nothing, as fast as they can be opened.
			for i := 0; i < 64; i++ {
				s, err := c.OpenStream()
				if err != nil {
					break
				}
				_, _ = s.Write([]byte{0}) // a stream exists for the peer once it carries a byte
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("could not reach the QUIC listener: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	select {
	case <-conn.Context().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a peer holding 64 unauthenticated streams was left connected")
	}
}
