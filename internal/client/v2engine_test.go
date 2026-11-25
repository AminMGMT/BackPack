package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/server"
	"github.com/backpack/backpack/internal/utils/network"
)

// Both ends on the v2.0.0 engine — what a pair runs while one of its servers
// is still on Backpack v2.0.0 (see compat.go on both sides).
//
// The kharej here runs that engine directly, as it does once it has found a
// v2.0.0 Iran server, and the Iran server is one that remembers serving a
// v2.0.0 kharej (its .compat pin), so neither end tries the plain engine and
// the whole tunnel — control channel, pool, sessions, relay — is the v2.0.0
// engine's. Nothing else runs that engine in-process: everything else that
// starts tcp, tcpmux, ws or wsmux reaches the plain engine.
func TestBothEndsOnTheV2EngineCarryATunnel(t *testing.T) {
	if testing.Short() {
		t.Skip("runs both ends of four tunnels")
	}
	for _, tr := range []config.TransportType{config.TCP, config.TCPMUX, config.WS, config.WSMUX} {
		t.Run(string(tr), func(t *testing.T) {
			t.Parallel()
			backend := v2EchoServer(t)
			entry, tunnel := v2FreePort(t), v2FreePort(t)
			const token = "v2-pair-token-0123456789abcdefgh"

			// The Iran server's pin says its kharej is on v2.0.0.
			dir := t.TempDir()
			peerPin := filepath.Join(dir, ".tunnel.peer")
			if err := os.WriteFile(filepath.Join(dir, ".tunnel.compat"), []byte("v2\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			srv := server.NewServer(&config.ServerConfig{
				BindAddr:  fmt.Sprintf("127.0.0.1:%d", tunnel),
				Transport: tr, Token: token, PeerPinPath: peerPin,
				Nodelay: true, Keepalive: 75, ChannelSize: 2048, Heartbeat: 20,
				MuxCon: 8, MuxSession: 8, MuxVersion: 2, MaxFrameSize: 32768,
				MaxReceiveBuffer: 4 << 20, MaxStreamBuffer: 65536,
				LogLevel: "fatal", SkipOptz: true,
				Ports: []string{fmt.Sprintf("%d=%s", entry, backend)},
			}, ctx)
			srvDone := make(chan struct{})
			go func() { defer close(srvDone); srv.Start() }()

			cfg := &config.ClientConfig{
				RemoteAddr: fmt.Sprintf("127.0.0.1:%d", tunnel), Transport: tr, Token: token,
				ConnectionPool: 4, RetryInterval: 1, Nodelay: true, Keepalive: 8,
				LogLevel: "fatal", MuxSession: 8, MuxVersion: 2, MaxFrameSize: 32768,
				MaxReceiveBuffer: 4 << 20, MaxStreamBuffer: 65536, DialTimeout: 5,
				SkipOptz: true,
			}
			t.Cleanup(func() {
				cancel()
				<-srvDone
			})
			// The kharej runs under a context of its own, so it can be taken
			// down and brought back while the Iran server stays up.
			kharej := func() (runner, context.CancelFunc) {
				kctx, stop := context.WithCancel(ctx)
				r := NewClient(cfg, kctx).startEngine(kctx, tr, network.NewEndpoints(cfg.RemoteAddr), nil)
				if r == nil {
					stop()
					t.Fatalf("%s: the kharej's v2.0.0 engine did not start", tr)
				}
				return r, stop
			}
			r, stopKharej := kharej()

			addr := fmt.Sprintf("127.0.0.1:%d", entry)
			if err := v2EchoWithin(addr, []byte("both ends on v2.0.0"), 40*time.Second); err != nil {
				t.Fatalf("%s: the tunnel never carried traffic: %v", tr, err)
			}
			if !r.Running() {
				t.Fatalf("%s: traffic flowed but the kharej's v2.0.0 engine is not connected", tr)
			}
			errs := make(chan error, 8)
			for i := range 8 {
				go func() { errs <- v2EchoOnce(addr, bytes.Repeat([]byte{byte(i + 1)}, 96<<10)) }()
			}
			for range 8 {
				if err := <-errs; err != nil {
					t.Fatalf("%s: a concurrent connection failed: %v", tr, err)
				}
			}
			// The kharej restarts — an update, a reboot — and the Iran server,
			// still up, takes it back on the same engine.
			stopKharej()
			time.Sleep(2 * time.Second)
			r, stopKharej = kharej()
			defer stopKharej()
			if err := v2EchoWithin(addr, []byte("after the kharej came back"), 40*time.Second); err != nil {
				t.Fatalf("%s: the tunnel did not come back after the kharej restarted: %v", tr, err)
			}
			if !r.Running() {
				t.Fatalf("%s: traffic flowed but the restarted kharej is not connected", tr)
			}

			// The server never left the v2.0.0 engine: the pin still says so.
			if b, err := os.ReadFile(filepath.Join(dir, ".tunnel.compat")); err != nil || string(b) != "v2\n" {
				t.Fatalf("%s: the Iran server moved off the v2.0.0 engine (pin %q, %v)", tr, b, err)
			}
		})
	}
}

func v2FreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func v2EchoServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return l.Addr().String()
}

func v2EchoWithin(addr string, payload []byte, within time.Duration) error {
	deadline := time.Now().Add(within)
	var last error
	for time.Now().Before(deadline) {
		if last = v2EchoOnce(addr, payload); last == nil {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return last
}

func v2EchoOnce(addr string, payload []byte) error {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	go func() { _, _ = c.Write(payload) }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(c, got); err != nil {
		return err
	}
	if !bytes.Equal(got, payload) {
		return fmt.Errorf("the echo came back changed")
	}
	return nil
}
