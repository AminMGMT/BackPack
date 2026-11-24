package controlwire

import (
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func tcpPair(t *testing.T) (a, b net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); got <- c }()
	a, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b = <-got
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

// A stream delivers a signal as the one byte it is.
func TestASignalCrossesTheCarrierIntact(t *testing.T) {
	a, b := tcpPair(t)
	for name, pair := range map[string][2]Link{
		"stream": {Net(a), Net(b)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := pair[0].Send(7); err != nil {
				t.Fatalf("send: %v", err)
			}
			_ = pair[1].SetReadDeadline(time.Now().Add(2 * time.Second))
			got, err := pair[1].Receive()
			if err != nil || got != 7 {
				t.Fatalf("received %d, %v; want 7", got, err)
			}
		})
	}
}

// A control channel is absent for the whole window between a drop and the
// next handshake, and the heartbeat keeps firing through it: every operation
// on an absent one is an error, never a panic.
func TestAnAbsentChannelIsAnErrorNotAPanic(t *testing.T) {
	for name, l := range map[string]Link{"stream": Net(nil)} {
		t.Run(name, func(t *testing.T) {
			if err := l.Send(1); !errors.Is(err, ErrNoChannel) {
				t.Errorf("a send on no channel: %v, want ErrNoChannel", err)
			}
			if _, err := l.Receive(); !errors.Is(err, ErrNoChannel) {
				t.Errorf("a receive on no channel: %v, want ErrNoChannel", err)
			}
			if err := l.SetReadDeadline(time.Now()); !errors.Is(err, ErrNoChannel) {
				t.Errorf("a deadline on no channel: %v, want ErrNoChannel", err)
			}
			l.Close()
		})
	}
}

// The far end going away is told apart from a channel that failed.
func TestAPeerClosingIsToldApartFromAFailure(t *testing.T) {
	for _, err := range []error{
		io.EOF,
		fmt.Errorf("failed to read message from net.Conn: %w", io.EOF),
		io.ErrUnexpectedEOF,
		&net.OpError{Op: "read", Err: syscall.ECONNRESET},
		&websocket.CloseError{Code: websocket.CloseAbnormalClosure, Text: "unexpected EOF"},
		errors.New("failed to read message from net.Conn: Application error 0x0 (remote): generation ended"),
	} {
		if !PeerClosed(err) {
			t.Errorf("%v was not read as the peer closing", err)
		}
	}
	for _, err := range []error{
		nil,
		errors.New("i/o timeout"),
		&net.OpError{Op: "read", Err: syscall.EHOSTUNREACH},
		&websocket.CloseError{Code: websocket.CloseProtocolError},
	} {
		if PeerClosed(err) {
			t.Errorf("%v was read as the peer closing", err)
		}
	}
}
