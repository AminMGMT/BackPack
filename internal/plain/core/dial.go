package core

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// TcpDialer dials remoteAddress, retrying retry times with a doubling backoff
// that starts at a second.
func TcpDialer(ctx context.Context, remoteAddress string, localSrc string, timeout time.Duration, keepAlive time.Duration, nodelay bool, retry int, SO_RCVBUF int, SO_SNDBUF, mss int) (*net.TCPConn, error) {
	var tcpConn *net.TCPConn
	var err error

	retries := retry
	backoff := 1 * time.Second

	for i := 0; i < retries; i++ {
		tcpConn, err = attemptTcpDialer(ctx, remoteAddress, localSrc, timeout, keepAlive, nodelay, SO_RCVBUF, SO_SNDBUF, mss)
		if err == nil {
			return tcpConn, nil
		}
		if i == retries-1 {
			break
		}
		time.Sleep(backoff)
		backoff *= 2
	}

	return nil, err
}

func attemptTcpDialer(ctx context.Context, remoteAddress string, localSrc string, timeout time.Duration, keepAlive time.Duration, nodelay bool, SO_RCVBUF int, SO_SNDBUF int, mss int) (*net.TCPConn, error) {
	tcpAddr, err := net.ResolveTCPAddr("tcp", remoteAddress)
	if err != nil {
		return nil, fmt.Errorf("DNS resolution: %v", err)
	}

	var localTCPAddr *net.TCPAddr
	if localSrc != "" {
		localTCPAddr, err = net.ResolveTCPAddr("tcp", localSrc)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve local address: %v", err)
		}
	}

	dialer := &net.Dialer{
		Control: func(network, address string, s syscall.RawConn) error {
			err := ReusePortControl(network, address, s)
			if err != nil {
				return err
			}

			if SO_RCVBUF > 0 {
				err = s.Control(func(fd uintptr) {
					if err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, SO_RCVBUF); err != nil {
						err = fmt.Errorf("failed to set SO_RCVBUF: %v", err)
					}
				})
			}
			if err != nil {
				return err
			}

			if SO_SNDBUF > 0 {
				err = s.Control(func(fd uintptr) {
					if err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, SO_SNDBUF); err != nil {
						err = fmt.Errorf("failed to set SO_SNDBUF: %v", err)
					}
				})
			}

			if mss > 0 {
				err = s.Control(func(fd uintptr) {
					if err = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_MAXSEG, mss); err != nil {
						err = fmt.Errorf("failed to set MSS: %v", err)
					}
				})
			}

			return err
		},
		Timeout:         timeout,
		KeepAliveConfig: net.KeepAliveConfig{Enable: true, Interval: keepAlive, Count: 9, Idle: 0},
		LocalAddr:       localTCPAddr,
	}

	conn, err := dialer.DialContext(ctx, "tcp", tcpAddr.String())
	if err != nil {
		return nil, err
	}

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

// ReusePortControl sets SO_REUSEADDR, and SO_REUSEPORT on Linux.
func ReusePortControl(network, address string, s syscall.RawConn) error {
	var controlErr error

	err := s.Control(func(fd uintptr) {
		if err := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
			controlErr = fmt.Errorf("failed to set SO_REUSEADDR: %v", err)
			return
		}
		if runtime.GOOS == "linux" {
			if err := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, 0xf /* SO_REUSEPORT */, 1); err != nil {
				controlErr = fmt.Errorf("failed to set SO_REUSEPORT: %v", err)
				return
			}
		}
	})
	if err != nil {
		return err
	}
	return controlErr
}

// ResolveRemoteAddr turns what the server names into a port and an address to
// dial: a bare port means 127.0.0.1.
func ResolveRemoteAddr(remoteAddr string) (int, string, error) {
	parts := strings.Split(remoteAddr, ":")
	var port int
	var err error

	if len(parts) < 2 {
		port, err = strconv.Atoi(parts[0])
		if err != nil {
			return 0, "", fmt.Errorf("invalid port format: %v", err)
		}
		return port, fmt.Sprintf("127.0.0.1:%d", port), nil
	}

	port, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, "", fmt.Errorf("invalid port format: %v", err)
	}
	return port, remoteAddr, nil
}
