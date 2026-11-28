package core

import (
	"bytes"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/sirupsen/logrus"
)

func stealth(token string) *Layer {
	return NewLayer(config.EncryptionConfig{Encryption: config.EncStealth}, token)
}

func TestNoneIsNoLayer(t *testing.T) {
	if NewLayer(config.EncryptionConfig{}, "t") != nil || NewLayer(config.EncryptionConfig{Encryption: "none"}, "t") != nil {
		t.Fatal("none built a layer")
	}
	var l *Layer
	a, b := net.Pipe()
	defer b.Close()
	if c, err := l.Client(a); err != nil || c != a {
		t.Fatal("a nil layer did not pass the connection through")
	}
	if l.Bearer("tok") != "tok" || !l.BearerOK("Bearer tok", "tok") {
		t.Fatal("a nil layer changed the plain bearer")
	}
}

// The listener hands over only connections that completed the handshake; a
// probe is closed without a byte, and does not hold the next client back.
func TestLayerListenerDropsProbesAndServesClients(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := stealth("tunnel-token")
	ln := l.Listener(raw, logrus.New())
	defer ln.Close()

	probe, err := net.Dial("tcp", raw.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	_, _ = probe.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()

	c, err := net.Dial("tcp", raw.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client, err := l.Client(c)
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	defer client.Close()
	var server net.Conn
	select {
	case server = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the listener never handed over a client that completed the handshake")
	}
	defer server.Close()
	if _, ok := TCPOf(server); !ok {
		t.Fatal("the accepted connection does not unwrap to its TCP socket")
	}
	go func() { _, _ = client.Write([]byte("ping")) }()
	buf := make([]byte, 4)
	if _, err := io.ReadFull(server, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("read %q, %v", buf, err)
	}

	_ = probe.SetReadDeadline(time.Now().Add(3 * time.Second))
	got, _ := io.ReadAll(probe)
	if len(got) != 0 {
		t.Fatalf("a probe was answered with %q", got)
	}
}

// With an encryption the websocket bearer is not the token.
func TestLayerBearerHidesTheToken(t *testing.T) {
	l := stealth("tunnel-token")
	b := l.Bearer("tunnel-token")
	if b == "tunnel-token" || bytes.Contains([]byte(b), []byte("tunnel")) {
		t.Fatalf("bearer %q reveals the token", b)
	}
	if !l.BearerOK("Bearer "+b, "tunnel-token") || l.BearerOK("Bearer tunnel-token", "tunnel-token") {
		t.Fatal("the bearer check takes the wrong value")
	}
}

// failingListener fails its first accepts the way a process out of file
// descriptors does, then accepts normally.
type failingListener struct {
	net.Listener
	failures int
}

func (f *failingListener) Accept() (net.Conn, error) {
	if f.failures > 0 {
		f.failures--
		return nil, &net.OpError{Op: "accept", Net: "tcp", Err: syscall.EMFILE}
	}
	return f.Listener.Accept()
}

// Running out of descriptors is waited out: the listener keeps serving once
// accepts work again, instead of closing the tunnel port until a restart.
func TestLayerListenerOutlastsRunningOutOfDescriptors(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := stealth("tunnel-token")
	ln := l.Listener(&failingListener{Listener: raw, failures: 5}, logrus.New())
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- c
		}
	}()
	c, err := net.Dial("tcp", raw.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client, err := l.Client(c)
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	defer client.Close()
	select {
	case server := <-accepted:
		server.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("the listener stopped serving after accepts failed for a moment")
	}

	ln.Close()
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("a closed listener reports %v", err)
	}
}
