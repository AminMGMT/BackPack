package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/xtaci/smux"
)

// echoService is the local service a user asked for: it echoes what it reads.
func echoService(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().String()
}

// A mux session as the client serves it: the server side opens streams and
// names a target on each, the client relays each to its backend.
func TestEachStreamOnASessionReachesTheBackendItNames(t *testing.T) {
	target := echoService(t)
	serverEnd, clientEnd := net.Pipe()
	defer serverEnd.Close()

	var l lifecycle
	l.firstGeneration(context.Background(), silentLogger(), usageSpec{})

	clientSession, err := smux.Server(clientEnd, smux.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	go l.serveSession(clientSession, clientEnd.RemoteAddr(), backendOpts{dialTimeout: time.Second, keepAlive: time.Second})

	serverSession, err := smux.Client(serverEnd, smux.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}

	// A stream that never names its target must not hold up the ones behind
	// it: the target is read in the stream's own goroutine.
	silent, err := serverSession.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()

	for i := 0; i < 3; i++ {
		s, err := serverSession.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		if err := utils.SendBinaryString(s, target); err != nil {
			t.Fatal(err)
		}
		msg := []byte("hello")
		if _, err := s.Write(msg); err != nil {
			t.Fatal(err)
		}
		_ = s.SetReadDeadline(time.Now().Add(3 * time.Second))
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(s, got); err != nil {
			t.Fatalf("stream %d: nothing came back from the backend: %v", i, err)
		}
		if string(got) != "hello" {
			t.Fatalf("stream %d: got %q", i, got)
		}
		s.Close()
	}
}

// A backend that refuses is the user's connection closed, not a hang.
func TestARefusedBackendClosesTheUsersConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	ln.Close()

	var l lifecycle
	l.firstGeneration(context.Background(), silentLogger(), usageSpec{})
	user, tunnel := net.Pipe()
	done := make(chan struct{})
	go func() { l.relayStream(tunnel, dead, backendOpts{dialTimeout: time.Second}); close(done) }()

	_ = user.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := user.Read(make([]byte, 1)); err == nil {
		t.Fatal("the user's connection stayed open with nothing behind it")
	}
	<-done
}

// Raw UDP uses the same DNS budget for its tunnel endpoint and local backend.
func TestRawUDPBackendAndEndpointDNSRespectCancellationAndTimeout(t *testing.T) {
	for _, backendPhase := range []bool{false, true} {
		for _, cancelGeneration := range []bool{true, false} {
			name := "endpoint"
			if backendPhase {
				name = "backend"
			}
			if cancelGeneration {
				name += "/cancel"
			} else {
				name += "/timeout"
			}
			t.Run(name, func(t *testing.T) {
				previous := net.DefaultResolver
				release := make(chan struct{})
				started := make(chan struct{}, 1)
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
					select {
					case started <- struct{}{}:
					default:
					}
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-release:
						return nil, errors.New("resolver released")
					}
				}}
				ctx, cancel := context.WithCancel(context.Background())
				timeout := 40 * time.Millisecond
				if cancelGeneration {
					timeout = 5 * time.Second
				}
				config := &UdpConfig{RemoteAddr: "raw-endpoint.invalid:53", Endpoints: network.NewEndpoints("raw-endpoint.invalid:53"), DialTimeOut: timeout, ConnPoolSize: 1}
				c := &UdpTransport{config: config}
				c.logger = silentLogger()
				c.state.Reset(ctx, cancel, nil)
				run := c.tunnelDialer
				if backendPhase {
					listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
					if err != nil {
						t.Fatal(err)
					}
					defer listener.Close()
					tunnel, err := net.DialUDP("udp", nil, listener.LocalAddr().(*net.UDPAddr))
					if err != nil {
						t.Fatal(err)
					}
					defer tunnel.Close()
					run = func() { c.localDialer("raw-backend.invalid:53", 53, tunnel) }
				}
				done := make(chan struct{})
				go func() { defer close(done); run() }()
				defer func() {
					cancel()
					unblock()
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("raw UDP DNS worker did not stop")
					}
					net.DefaultResolver = previous
				}()
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("raw UDP DNS did not start")
				}
				if cancelGeneration {
					cancel()
				}
				select {
				case <-done:
				case <-time.After(300 * time.Millisecond):
					t.Fatal("raw UDP DNS ignored cancellation or dial timeout")
				}
			})
		}
	}
}

// DNS is part of the UDP backend dial budget, and cancellation must not leave
// an old generation waiting for a resolver before it can reconnect.
func TestUDPBackendDNSObeysGenerationAndDialTimeout(t *testing.T) {
	for _, cancelGeneration := range []bool{true, false} {
		name := "timeout"
		if cancelGeneration {
			name = "generation"
		}
		t.Run(name, func(t *testing.T) {
			previous := net.DefaultResolver
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				select {
				case started <- struct{}{}:
				default:
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
					return nil, errors.New("resolver released")
				}
			}}
			ctx, cancel := context.WithCancel(context.Background())
			var l lifecycle
			l.firstGeneration(ctx, silentLogger(), usageSpec{})
			user, stream := net.Pipe()
			done := make(chan struct{})
			timeout := 40 * time.Millisecond
			if cancelGeneration {
				timeout = 5 * time.Second
			}
			go func() {
				defer close(done)
				l.relayStream(stream, "udp://stalled.backend.invalid:53", backendOpts{dialTimeout: timeout})
			}()
			defer func() {
				cancel()
				unblock()
				stream.Close()
				user.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("UDP worker did not stop")
				}
				net.DefaultResolver = previous
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("DNS lookup did not start")
			}
			if cancelGeneration {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(300 * time.Millisecond):
				t.Fatal("UDP backend DNS ignored generation cancellation or dial timeout")
			}
			user.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := user.Read(make([]byte, 1)); err == nil {
				t.Fatal("failed UDP dial left tunnel open")
			}
		})
	}
}

// A generation ending must also join an already connected, idle UDP flow.
func TestUDPBackendIdleFlowEndsWithGeneration(t *testing.T) {
	backend, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var l lifecycle
	l.firstGeneration(ctx, silentLogger(), usageSpec{})
	user, stream := net.Pipe()
	defer user.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.relayStream(stream, "udp://"+backend.LocalAddr().String(), backendOpts{dialTimeout: time.Second})
	}()
	defer func() {
		cancel()
		stream.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("idle UDP flow did not stop")
		}
	}()
	user.SetDeadline(time.Now().Add(time.Second))
	payload := []byte("connected before cancellation")
	if err := network.WriteDatagram(user, payload); err != nil {
		t.Fatal(err)
	}
	backend.SetReadDeadline(time.Now().Add(time.Second))
	received := make([]byte, network.MaxDatagram)
	n, from, err := backend.ReadFromUDP(received)
	if err != nil || !bytes.Equal(received[:n], payload) {
		t.Fatalf("UDP backend payload=%q err=%v", received[:n], err)
	}
	if _, err := backend.WriteToUDP(received[:n], from); err != nil {
		t.Fatal(err)
	}
	n, err = network.ReadDatagram(user, received)
	if err != nil || !bytes.Equal(received[:n], payload) {
		t.Fatalf("UDP tunnel reply=%q err=%v", received[:n], err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("idle UDP flow outlived its generation")
	}
	if _, err := user.Read(make([]byte, 1)); err == nil {
		t.Fatal("canceled UDP flow left tunnel open")
	}
}

// A session belongs to its generation: when the generation ends, the session
// is closed and its loop returns. Over KCP nothing else would end it — the
// churn test found thirty-two of them left behind by eight clients.
func TestASessionEndsWithItsGeneration(t *testing.T) {
	serverEnd, clientEnd := net.Pipe()
	defer serverEnd.Close()

	parent, end := context.WithCancel(context.Background())
	var l lifecycle
	l.firstGeneration(parent, silentLogger(), usageSpec{})

	session, err := smux.Server(clientEnd, smux.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := smux.Client(serverEnd, smux.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { l.serveSession(session, clientEnd.RemoteAddr(), backendOpts{}); close(done) }()

	end()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the session outlived its generation")
	}
	if !session.IsClosed() {
		t.Fatal("the loop returned but the session is still open")
	}
}

// UDP uses stream framing on every reverse transport. Growing the receive
// buffer must preserve empty packets, split frames and large UDP payloads.
func TestUDPBackendReceivesEveryFramedDatagram(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	backend, err := net.DialUDP("udp", nil, listener.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	user, stream := net.Pipe()
	defer user.Close()
	defer stream.Close()
	done := make(chan struct{})
	go func() { defer close(done); tunnelToBackend(stream, backend, silentLogger(), nil, 0, false) }()
	t.Cleanup(func() {
		stream.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("UDP receive worker did not stop")
		}
	})
	received := make([]byte, network.MaxDatagram)
	for _, size := range []int{0, 1, 1200, 16385, 64000, 7, 0, 1200} {
		payload := bytes.Repeat([]byte{byte(size)}, size)
		var frame bytes.Buffer
		if err := network.WriteDatagram(&frame, payload); err != nil {
			t.Fatal(err)
		}
		// A length prefix can span reads, and frames need not line up with reads.
		wire := frame.Bytes()
		for _, part := range [][]byte{wire[:1], wire[1:2], wire[2:]} {
			if len(part) > 0 {
				if _, err := user.Write(part); err != nil {
					t.Fatal(err)
				}
			}
		}
		listener.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := listener.ReadFromUDP(received)
		if err != nil || !bytes.Equal(received[:n], payload) {
			t.Fatalf("payload size%d received%d err%v", size, n, err)
		}
	}
	user.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("EOF did not end UDP receive worker")
	}
}
