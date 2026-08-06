package transport

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/backpack/backpack/internal/web"
	"github.com/sirupsen/logrus"
)

var forwardBackendCursor sync.Map // map[canonical backend list]*atomic.Uint64

// dialForwardTCPBackend load-balances across the same pipe-separated backend
// syntax supported by reverse tunnels. A failed member is skipped immediately;
// the next user connection starts at the next member, so healthy backends share
// load without a dead member black-holing the ingress.
func dialForwardTCPBackend(ctx context.Context, target string, keepAlive time.Duration) (net.Conn, int, string, error) {
	firstPort, resolved, err := network.ResolveRemoteAddr(target)
	if err != nil {
		return nil, 0, "", err
	}
	parts := strings.Split(resolved, "|")
	cursorAny, _ := forwardBackendCursor.LoadOrStore(resolved, &atomic.Uint64{})
	start := int(cursorAny.(*atomic.Uint64).Add(1)-1) % len(parts)
	var lastErr error
	for i := 0; i < len(parts); i++ {
		candidate := strings.TrimSpace(parts[(start+i)%len(parts)])
		port, _, err := network.ResolveRemoteAddr(candidate)
		if err != nil {
			lastErr = err
			continue
		}
		dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: keepAlive}
		backend, err := dialer.DialContext(ctx, "tcp", candidate)
		if err == nil {
			return backend, port, candidate, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no backend candidates")
	}
	return nil, firstPort, "", lastErr
}

// handleForwardStream is shared by every stream-oriented carrier. The carrier
// has already authenticated the data connection/session; this function reads
// the requested Kharej backend, dials it, acknowledges readiness, and relays.
func handleForwardStream(ctx context.Context, stream net.Conn, keepAlive time.Duration, logger *logrus.Logger, usage *web.Usage, sniffer bool) {
	target, err := utils.ReceiveBinaryString(stream)
	if err != nil {
		logger.Warnf("invalid forward target from %s: %v", stream.RemoteAddr(), err)
		_ = utils.SendBinaryByte(stream, utils.SG_ForwardError)
		stream.Close()
		return
	}
	backend, port, resolved, err := dialForwardTCPBackend(ctx, target, keepAlive)
	if err != nil {
		logger.Warnf("invalid forward backend %q: %v", target, err)
		_ = utils.SendBinaryByte(stream, utils.SG_ForwardError)
		stream.Close()
		return
	}
	if err := utils.SendBinaryByte(stream, utils.SG_ForwardOK); err != nil {
		backend.Close()
		stream.Close()
		return
	}
	logger.Debugf("forward data channel connected to backend %s", resolved)
	handlers.TCPConnectionHandler(ctx, false, metrics.CountedConn(stream), backend, logger, usage, port, sniffer)
}
