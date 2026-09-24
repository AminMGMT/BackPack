package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/backpack/backpack/internal/server"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"
)

// The reverse QUIC transport does not verify the server's certificate, so
// anything on the path can terminate the TLS. It used to be handed the token
// for its trouble: the client sent it as the control claim and on every data
// stream, and the server echoed it back. These put a real man in the middle —
// one that terminates QUIC on both sides and relays every stream byte for
// byte — and check that it learns nothing and gets nowhere.

// quicMITM accepts the client's QUIC connection with its own certificate, dials
// the real server with a second connection, and pipes each stream the client
// opens onto a fresh stream to the server. Everything the client sends is kept.
type quicMITM struct {
	addr string
	mu   sync.Mutex
	seen bytes.Buffer
}

func (m *quicMITM) saw() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.seen.Bytes()...)
}

type recordingWriter struct{ m *quicMITM }

func (w recordingWriter) Write(p []byte) (int, error) {
	w.m.mu.Lock()
	w.m.seen.Write(p)
	w.m.mu.Unlock()
	return len(p), nil
}

func startQUICMITM(t *testing.T, upstream string) *quicMITM {
	t.Helper()
	ln, err := network.QUICListen("127.0.0.1:0", network.QUICSettings{MaxIdleTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); ln.Close() })
	m := &quicMITM{addr: ln.Addr().String()}

	go func() {
		for {
			in, err := ln.Accept(ctx)
			if err != nil {
				return
			}
			go func(in *quic.Conn) {
				out, err := network.QUICDial(ctx, upstream, network.QUICSettings{MaxIdleTimeout: 30 * time.Second})
				if err != nil {
					in.CloseWithError(0, "")
					return
				}
				for {
					s, err := in.AcceptStream(ctx)
					if err != nil {
						out.CloseWithError(0, "")
						return
					}
					u, err := out.OpenStreamSync(ctx)
					if err != nil {
						s.Close()
						continue
					}
					go func() { io.Copy(io.MultiWriter(u, recordingWriter{m}), s); u.Close() }()
					go func() { io.Copy(s, u); s.Close() }()
				}
			}(in)
		}
	}()
	return m
}

func startQUICServer(t *testing.T, tunnelPort, entryPort int, backend, token string) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	srv := server.NewServer(baseServerConfig("quic", tunnelPort, entryPort, backend, token), ctx)
	wg.Add(1)
	go func() { defer wg.Done(); srv.Start() }()
	t.Cleanup(func() { stop(); srv.Stop(); wg.Wait() })
	time.Sleep(300 * time.Millisecond)
}

func TestAQUICManInTheMiddleNeverSeesTheToken(t *testing.T) {
	backend := startEchoBackend(t)
	tunnelPort, entryPort := freePort(t), freePort(t)
	const token = "quic-mitm-token-must-never-leak-0123"
	startQUICServer(t, tunnelPort, entryPort, backend.addr, token)

	mitm := startQUICMITM(t, fmt.Sprintf("127.0.0.1:%d", tunnelPort))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tun := startClientAgainst(t, ctx, baseClientConfig("quic", mitm.addr, token, nil), entryPort, tunnelPort)

	// Long enough for several full claim attempts through the relay.
	if err := tun.waitReady(8 * time.Second); err == nil {
		t.Fatal("the tunnel came up through a man in the middle that holds a different TLS session")
	}
	seen := mitm.saw()
	if len(seen) == 0 {
		t.Fatal("the relay saw nothing, so this proved nothing — the client never reached it")
	}
	if bytes.Contains(seen, []byte(token)) {
		t.Fatalf("the man in the middle read the token off the wire (%d bytes relayed)", len(seen))
	}
}

// Without anything in between, the bound handshake works end to end.
func TestAQUICTunnelWithoutAManInTheMiddleComesUp(t *testing.T) {
	backend := startEchoBackend(t)
	tunnelPort, entryPort := freePort(t), freePort(t)
	const token = "quic-direct-token-0123456789abcd"
	startQUICServer(t, tunnelPort, entryPort, backend.addr, token)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tun := startClientAgainst(t, ctx,
		baseClientConfig("quic", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil), entryPort, tunnelPort)
	if err := tun.waitReady(tunnelReadyTimeout); err != nil {
		t.Fatalf("a bound quic tunnel never came up: %v", err)
	}
	if err := tun.roundTrip(randomPayload(t, 64*1024)); err != nil {
		t.Fatal(err)
	}
}

// A client from before the binding sends the token itself, and a new server
// still takes it — which is what lets the server be upgraded first. It must get
// the token back, which is the answer that client checks for.
func TestANewQUICServerStillAcceptsAnOldClient(t *testing.T) {
	backend := startEchoBackend(t)
	tunnelPort, entryPort := freePort(t), freePort(t)
	const token = "quic-legacy-token-0123456789abcd"
	startQUICServer(t, tunnelPort, entryPort, backend.addr, token)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := network.QUICDial(ctx, fmt.Sprintf("127.0.0.1:%d", tunnelPort), network.QUICSettings{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseWithError(0, "")
	st, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	control := network.NewQUICStreamConn(st, conn)
	if err := utils.SendBinaryTransportString(control, token, utils.SG_Chan); err != nil {
		t.Fatal(err)
	}
	control.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, sig, err := utils.ReceiveBinaryTransportString(control)
	if err != nil {
		t.Fatalf("an old-style claim got no answer: %v", err)
	}
	if sig != utils.SG_Chan || got != token {
		t.Fatalf("an old-style claim got %q (signal %d), want the token back", got, sig)
	}
	_ = net.Conn(control)
}
