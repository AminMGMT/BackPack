//go:build !linux

package manage

import (
	"errors"
	"net"
)

func ctDontFragment(*net.UDPConn) error { return errors.New("path MTU probing needs Linux") }
