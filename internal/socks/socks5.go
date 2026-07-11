// Package socks implements a minimal SOCKS5 CONNECT proxy (server + client)
// with username/password auth and no third-party dependencies.
//
// In backpack it lets a node that can't reach a destination (e.g. the Iran
// server reaching Telegram) tunnel through a peer that can: the peer runs the
// SOCKS5 server bound to localhost, that port is exposed over the reverse
// tunnel, and the origin dials the proxy through the tunnel.
package socks

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	ver5     = 0x05
	authUP   = 0x02 // username/password
	authNone = 0x00
	cmdConn  = 0x01
	atypIPv4 = 0x01
	atypHost = 0x03
	atypIPv6 = 0x04
	repOK    = 0x00
)

// AuthFunc validates a username/password pair.
type AuthFunc func(user, pass string) bool

// Serve runs a SOCKS5 CONNECT server on addr until ctx is cancelled. Every
// connection must authenticate with username/password accepted by auth.
func Serve(ctx context.Context, addr string, auth AuthFunc) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				continue
			}
		}
		go handle(conn, auth)
	}
}

func handle(conn net.Conn, auth AuthFunc) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	br := make([]byte, 2)
	if _, err := io.ReadFull(conn, br); err != nil || br[0] != ver5 {
		return
	}
	methods := make([]byte, int(br[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	// We require username/password auth.
	if !hasMethod(methods, authUP) {
		conn.Write([]byte{ver5, 0xFF})
		return
	}
	conn.Write([]byte{ver5, authUP})

	// Auth sub-negotiation.
	if !readAuth(conn, auth) {
		conn.Write([]byte{0x01, 0x01}) // failure
		return
	}
	conn.Write([]byte{0x01, 0x00}) // success

	// Request.
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil || head[0] != ver5 || head[1] != cmdConn {
		reply(conn, 0x07)
		return
	}
	host, err := readAddr(conn, head[3])
	if err != nil {
		reply(conn, 0x08)
		return
	}
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		reply(conn, 0x01)
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBuf))))

	remote, err := net.DialTimeout("tcp", target, 15*time.Second)
	if err != nil {
		reply(conn, 0x05)
		return
	}
	defer remote.Close()
	reply(conn, repOK)

	// Pipe both directions with no idle deadline.
	conn.SetDeadline(time.Time{})
	pipe(conn, remote)
}

func readAuth(conn net.Conn, auth AuthFunc) bool {
	h := make([]byte, 2)
	if _, err := io.ReadFull(conn, h); err != nil || h[0] != 0x01 {
		return false
	}
	user := make([]byte, int(h[1]))
	if _, err := io.ReadFull(conn, user); err != nil {
		return false
	}
	pl := make([]byte, 1)
	if _, err := io.ReadFull(conn, pl); err != nil {
		return false
	}
	pass := make([]byte, int(pl[0]))
	if _, err := io.ReadFull(conn, pass); err != nil {
		return false
	}
	return auth != nil && auth(string(user), string(pass))
}

func readAddr(conn net.Conn, atyp byte) (string, error) {
	switch atyp {
	case atypIPv4:
		b := make([]byte, 4)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	case atypIPv6:
		b := make([]byte, 16)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	case atypHost:
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			return "", err
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", err
		}
		return string(b), nil
	}
	return "", fmt.Errorf("unsupported address type")
}

func reply(conn net.Conn, code byte) {
	conn.Write([]byte{ver5, code, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0})
}

func hasMethod(methods []byte, m byte) bool {
	for _, x := range methods {
		if x == m {
			return true
		}
	}
	return false
}

// pipe copies data both ways. When one direction ends it half-closes the
// other's write side (so buffered data flushes and the peer sees a clean EOF),
// and only returns once BOTH directions are done — avoiding truncated responses.
func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		io.Copy(dst, src)
		closeWrite(dst)
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
}
