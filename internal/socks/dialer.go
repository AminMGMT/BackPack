package socks

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Dial connects to targetHost:targetPort through a SOCKS5 proxy at proxyAddr,
// authenticating with username/password.
func Dial(proxyAddr, user, pass, targetHost string, targetPort int) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", proxyAddr, 15*time.Second)
	if err != nil {
		return nil, err
	}
	if err := negotiate(conn, user, pass, targetHost, targetPort); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func negotiate(conn net.Conn, user, pass, host string, port int) error {
	conn.SetDeadline(time.Now().Add(20 * time.Second))

	// Greeting: offer username/password auth.
	if _, err := conn.Write([]byte{ver5, 0x01, authUP}); err != nil {
		return err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return err
	}
	if resp[0] != ver5 || resp[1] != authUP {
		return fmt.Errorf("socks5: proxy rejected username/password auth")
	}

	// Auth.
	auth := []byte{0x01, byte(len(user))}
	auth = append(auth, user...)
	auth = append(auth, byte(len(pass)))
	auth = append(auth, pass...)
	if _, err := conn.Write(auth); err != nil {
		return err
	}
	ar := make([]byte, 2)
	if _, err := io.ReadFull(conn, ar); err != nil {
		return err
	}
	if ar[1] != 0x00 {
		return fmt.Errorf("socks5: authentication failed")
	}

	// CONNECT request (domain address type).
	req := []byte{ver5, cmdConn, 0x00, atypHost, byte(len(host))}
	req = append(req, host...)
	p := make([]byte, 2)
	binary.BigEndian.PutUint16(p, uint16(port))
	req = append(req, p...)
	if _, err := conn.Write(req); err != nil {
		return err
	}

	// Reply: VER REP RSV ATYP ...
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return err
	}
	if head[1] != repOK {
		return fmt.Errorf("socks5: connect failed (code %d)", head[1])
	}
	// Consume bound address + port.
	switch head[3] {
	case atypIPv4:
		io.ReadFull(conn, make([]byte, 4+2))
	case atypIPv6:
		io.ReadFull(conn, make([]byte, 16+2))
	case atypHost:
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			return err
		}
		io.ReadFull(conn, make([]byte, int(l[0])+2))
	}
	conn.SetDeadline(time.Time{})
	return nil
}

// HTTPClient returns an *http.Client whose connections are tunnelled through
// the SOCKS5 proxy at proxyAddr using the given credentials.
func HTTPClient(proxyAddr, user, pass string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, addr string) (net.Conn, error) {
				host, portStr, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				port, _ := strconv.Atoi(portStr)
				return Dial(proxyAddr, user, pass, host, port)
			},
		},
	}
}
