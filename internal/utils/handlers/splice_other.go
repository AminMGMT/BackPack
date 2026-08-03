//go:build !linux

package handlers

import "net"

// spliceRelay is a no-op off Linux: splice(2) is a Linux syscall. Reporting
// "not handled" sends every connection down the buffered path, which is what
// this platform had anyway. Backpack's servers are Linux; this keeps local
// builds on macOS and Windows compiling and behaving.
func spliceRelay(dst, src *net.TCPConn, onChunk func(n int)) (handled bool, err error) {
	return false, nil
}
