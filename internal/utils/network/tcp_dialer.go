package network

import (
	"context"
	"fmt"
	"net"
	"syscall"
	"time"
)

func TcpDialer(ctx context.Context, remoteAddress string, localSrc string, timeout time.Duration, keepAlive time.Duration, nodelay bool, retry int, SO_RCVBUF int, SO_SNDBUF, mss int) (*net.TCPConn, error) {
	var tcpConn *net.TCPConn
	var err error

	retries := retry           // Number of retries
	backoff := 1 * time.Second // Initial backoff duration

	for i := 0; i < retries; i++ {
		// Attempt to establish a TCP connection
		tcpConn, err = attemptTcpDialer(ctx, remoteAddress, localSrc, timeout, keepAlive, nodelay, SO_RCVBUF, SO_SNDBUF, mss)
		if err == nil {
			// Connection successful
			return tcpConn, nil
		}

		// If this is the last retry, return the error
		if i == retries-1 {
			break
		}

		// Log retry attempt and wait before retrying
		time.Sleep(backoff)
		backoff *= 2 // Exponential backoff (double the wait time after each failure)
	}

	return nil, err
}

// func attemptTcpDialer(ctx context.Context, address string, timeout time.Duration, keepAlive time.Duration, nodelay bool, SO_RCVBUF int, SO_SNDBUF int) (*net.TCPConn, error) {
func attemptTcpDialer(
	ctx context.Context,
	remoteAddress string,
	localSrc string,
	timeout time.Duration,
	keepAlive time.Duration,
	nodelay bool,
	SO_RCVBUF int,
	SO_SNDBUF int,
	mss int,
) (*net.TCPConn, error) {

	// The address is deliberately not resolved here.
	//
	// It used to be, through net.ResolveTCPAddr, which returns exactly one
	// address — the first IPv4 one — and that address was then the only one
	// ever dialled. A server name with several A records is the ordinary way to
	// publish more than one route to the same machine, and it is what an
	// operator reaches for when one IP starts being filtered: add another,
	// leave the name alone. Resolving to a single address threw all of that
	// away. The first record was tried forever, and if it was the blocked one
	// the tunnel simply never connected, while every other record sat there
	// working.
	//
	// Handing the name to the dialer instead gets Go's own behaviour: it
	// resolves the lot and tries them in turn until one answers, splitting the
	// timeout across the attempts, and races IPv4 against IPv6 (RFC 6555) so a
	// host with an AAAA record that does not actually route no longer costs the
	// whole connection. This is what rathole and gost both do, by dialling the
	// name rather than an address.
	var localTCPAddr *net.TCPAddr
	if localSrc != "" {
		var err error
		localTCPAddr, err = net.ResolveTCPAddr("tcp", localSrc)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve local address: %v", err)
		}
	}

	// Options
	dialer := &net.Dialer{
		Control: func(network, address string, s syscall.RawConn) error {
			// No address-reuse options on an outgoing socket: it binds an
			// ephemeral port it has no reason to share, and asking used to
			// abort the dial outright wherever the kernel refused. See
			// reuse_port.go.
			var err error

			if PinTCPBuffers() && SO_RCVBUF > 0 {
				err = s.Control(func(fd uintptr) {
					if err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, SO_RCVBUF); err != nil {
						err = fmt.Errorf("failed to set SO_RCVBUF: %v", err)
					}
				})
			}
			if err != nil {
				return err
			}

			if PinTCPBuffers() && SO_SNDBUF > 0 {
				err = s.Control(func(fd uintptr) {
					if err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, SO_SNDBUF); err != nil {
						err = fmt.Errorf("failed to set SO_SNDBUF: %v", err)
					}
				})
			}
			if err != nil {
				return err
			}

			// BBR per socket, so the tunnel gets it even where the system
			// default is not BBR. Best effort — see setCongestion.
			_ = s.Control(func(fd uintptr) { setCongestion(fd, TCPCongestion) })

			// Set MSS (Maximum Segment Size)
			if mss > 0 {
				err = s.Control(func(fd uintptr) {
					if err = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_MAXSEG, mss); err != nil {
						err = fmt.Errorf("failed to set MSS: %v", err)
					}
				})
			}

			return err

		},
		Timeout:         timeout, // Set the connection timeout
		KeepAliveConfig: net.KeepAliveConfig{Enable: true, Interval: keepAlive, Count: 9, Idle: 0},
		LocalAddr:       localTCPAddr,
	}

	// Dial by name, so every address it resolves to gets a turn.
	conn, err := dialer.DialContext(ctx, "tcp", remoteAddress)
	if err != nil {
		return nil, err
	}

	// Type assert the net.Conn to *net.TCPConn
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("failed to convert net.Conn to *net.TCPConn")
	}

	if !nodelay {
		err = tcpConn.SetNoDelay(false)
		if err != nil {
			tcpConn.Close()
			return nil, fmt.Errorf("failed to set TCP_NODELAY to %v: %w", nodelay, err)
		}
	}

	return tcpConn, nil
}
