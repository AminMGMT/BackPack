package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/client"
)

// The v2.0.0 engine, which an updated Iran server keeps to serve a kharej not
// yet updated (compat.go), still carries a tunnel.
//
// Everything else that runs tcp, tcpmux, ws and wsmux in-process now reaches
// the plain engine, so without this the v2.0.0 engine — on this side and the
// kharej's — would be exercised only by the cross-version tests, which run
// built binaries. Here the Iran side runs that engine directly and refuses to
// move off it, and an ordinary client finds out the way it would from a real
// v2.0.0 server: its plain handshake is refused, it falls back to the v2.0.0
// engine, and traffic flows.
func TestTheV2EngineStillCarriesATunnel(t *testing.T) {
	if testing.Short() {
		t.Skip("runs both ends of four tunnels")
	}
	for _, tr := range []config.TransportType{config.TCP, config.TCPMUX, config.WS, config.WSMUX} {
		t.Run(string(tr), func(t *testing.T) {
			t.Parallel()
			backend := echoServer(t)
			entry := freePort(t)
			cfg := &config.ServerConfig{
				BindAddr:  fmt.Sprintf("127.0.0.1:%d", freePort(t)),
				Transport: tr, Token: "v2-engine-token-0123456789abcdef",
				Nodelay: true, Keepalive: 75, ChannelSize: 2048, Heartbeat: 20,
				MuxCon: 8, MuxSession: 8, MuxVersion: 2, MaxFrameSize: 32768,
				MaxReceiveBuffer: 4 << 20, MaxStreamBuffer: 65536,
				LogLevel: "fatal", SkipOptz: true,
				Ports: []string{fmt.Sprintf("%d=%s", entry, backend)},
			}
			ctx, cancel := context.WithCancel(context.Background())
			s := NewServer(cfg, ctx)
			// No onPlain: the engine answers a plain claim exactly as a v2.0.0
			// server does (too old over tcp, its decoy page over ws) and stays
			// on, as one that has never been updated would.
			r := s.startEngine(ctx, tr, nil)
			if r == nil {
				cancel()
				t.Fatalf("%s: the v2.0.0 engine did not start", tr)
			}

			cli := client.NewClient(&config.ClientConfig{
				RemoteAddr: cfg.BindAddr, Transport: tr, Token: cfg.Token,
				ConnectionPool: 4, RetryInterval: 1, Nodelay: true, Keepalive: 8,
				LogLevel: "fatal", MuxSession: 8, MuxVersion: 2, MaxFrameSize: 32768,
				MaxReceiveBuffer: 4 << 20, MaxStreamBuffer: 65536, DialTimeout: 5,
				SkipOptz: true,
			}, ctx)
			clientDone := make(chan struct{})
			go func() { defer close(clientDone); cli.Start() }()
			t.Cleanup(func() {
				cancel()
				<-clientDone
				wait, stop := context.WithTimeout(context.Background(), 15*time.Second)
				r.Wait(wait)
				stop()
			})

			addr := fmt.Sprintf("127.0.0.1:%d", entry)
			if err := echoWithin(addr, []byte("over the v2.0.0 engine"), 40*time.Second); err != nil {
				t.Fatalf("%s: the tunnel never carried traffic: %v", tr, err)
			}
			if !r.Running() {
				t.Fatalf("%s: traffic flowed but the v2.0.0 engine reports no kharej paired", tr)
			}

			// Several connections at once, each with a payload larger than one
			// read, so the relay and the session handling are exercised too.
			errs := make(chan error, 8)
			for i := range 8 {
				go func() {
					payload := bytes.Repeat([]byte{byte(i + 1)}, 96<<10)
					errs <- echoOnce(addr, payload)
				}()
			}
			for range 8 {
				if err := <-errs; err != nil {
					t.Fatalf("%s: a concurrent connection failed: %v", tr, err)
				}
			}
		})
	}
}

// echoServer is a backend that sends back whatever it is sent.
func echoServer(t *testing.T) string {
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

// echoWithin retries one round trip until it succeeds or within passes.
func echoWithin(addr string, payload []byte, within time.Duration) error {
	deadline := time.Now().Add(within)
	var last error
	for time.Now().Before(deadline) {
		if last = echoOnce(addr, payload); last == nil {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return last
}

// echoOnce sends payload through addr and checks it comes back whole.
func echoOnce(addr string, payload []byte) error {
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
