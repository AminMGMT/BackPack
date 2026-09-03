package snispoof

import (
	"bytes"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// Every hello is the same size, whatever domain is in it.
//
// A length that moved with the name would make one connection distinguishable
// from another by nothing more than the number of bytes on the wire, which
// undoes the reason for sending it.
func TestEveryHelloIsTheSameSize(t *testing.T) {
	for _, name := range []string{"a.ir", "mci.ir", "www.digikala.com", strings.Repeat("x", maxSNI)} {
		h, err := BuildHello(name)
		if err != nil {
			t.Fatalf("BuildHello(%q): %v", name, err)
		}
		if len(h) != helloSize {
			t.Errorf("BuildHello(%q) is %d bytes, want %d", name, len(h), helloSize)
		}
		got, ok := SNIOf(h)
		if !ok || got != name {
			t.Errorf("the hello names %q, want %q", got, name)
		}
	}
}

// Two hellos for the same domain must not be identical: the random, the session
// id and the key share are what stop a filter from matching the flows to each
// other by their bytes.
func TestTwoHellosDifferWhereTheyShould(t *testing.T) {
	a, err := BuildHello("mci.ir")
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildHello("mci.ir")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two hellos for one domain came out byte-identical")
	}
}

// It has to be a ClientHello a real TLS implementation will parse, or the
// middlebox this is addressed to will not read the name out of it either.
//
// crypto/tls is the strictest reader to hand: if Go's server accepts the
// message and reports the name, a DPI box's parser will too.
func TestARealTLSServerReadsTheNameOutOfIt(t *testing.T) {
	hello, err := BuildHello("mci.ir")
	if err != nil {
		t.Fatal(err)
	}
	if hello[0] != 0x16 {
		t.Errorf("record type = %#x, want handshake (0x16)", hello[0])
	}
	if hello[5] != 0x01 {
		t.Errorf("handshake type = %#x, want client_hello (0x01)", hello[5])
	}

	srv, cli := net.Pipe()
	seen := make(chan string, 1)
	go func() {
		defer srv.Close()
		_ = tls.Server(srv, &tls.Config{
			GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
				seen <- h.ServerName
				return nil, errors.New("that is all this test needed")
			},
		}).Handshake()
	}()
	go func() {
		cli.SetWriteDeadline(time.Now().Add(5 * time.Second))
		cli.Write(hello)
		cli.Close()
	}()

	select {
	case got := <-seen:
		if got != "mci.ir" {
			t.Errorf("crypto/tls read the name as %q, want mci.ir", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("crypto/tls never reached the server name — the hello is malformed")
	}
}
