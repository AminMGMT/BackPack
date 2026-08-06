package transport

import (
	"context"
	"net"
	"sync/atomic"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/web"
	"github.com/sirupsen/logrus"
)

type forwardStreamOpener func(target string) (net.Conn, error)

// startForwardIngress is the Iran-side TCP ingress shared by all stream
// carriers. Carrier implementations provide only how a new logical stream is
// opened; mapping, limits, PROXY protocol and relay semantics stay identical.
func startForwardIngress(ctx context.Context, specs []string, maxConnections, bandwidthMbps int, proxyProtocol bool, logger *logrus.Logger, usage *web.Usage, sniffer bool, active *int32, open forwardStreamOpener, restart func()) {
	mappings, err := expandForwardTCPMappings(specs)
	if err != nil {
		logger.Errorf("invalid forward ingress mappings: %v", err)
		restart()
		return
	}
	bandwidth := newForwardBandwidth(bandwidthMbps)
	for _, mapping := range mappings {
		mapping := mapping
		go func() {
			listener, err := net.Listen("tcp", mapping.listen)
			if err != nil {
				logger.Errorf("failed to listen on forward ingress %s: %v", mapping.listen, err)
				restart()
				return
			}
			defer listener.Close()
			go func() { <-ctx.Done(); _ = listener.Close() }()
			logger.Infof("forward ingress listening on %s -> Kharej %s", listener.Addr(), mapping.target)
			for {
				local, err := listener.Accept()
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					logger.Warnf("forward ingress accept on %s failed: %v", mapping.listen, err)
					continue
				}
				if !acquireForwardConnection(active, maxConnections) {
					logger.Warnf("forward connection limit reached, refusing %s", local.RemoteAddr())
					local.Close()
					continue
				}
				go func(local net.Conn) {
					defer atomic.AddInt32(active, -1)
					local = bandwidth.wrap(local)
					stream, err := open(mapping.target)
					if err != nil {
						logger.Warnf("could not open forward channel for %s: %v", mapping.target, err)
						local.Close()
						return
					}
					port := 0
					if addr, ok := local.LocalAddr().(*net.TCPAddr); ok {
						port = addr.Port
					}
					handlers.TCPConnectionHandler(ctx, proxyProtocol, local, metrics.CountedConn(stream), logger, usage, port, sniffer)
				}(local)
			}
		}()
	}
}

func acquireForwardConnection(active *int32, limit int) bool {
	if limit <= 0 {
		atomic.AddInt32(active, 1)
		return true
	}
	for {
		current := atomic.LoadInt32(active)
		if int(current) >= limit {
			return false
		}
		if atomic.CompareAndSwapInt32(active, current, current+1) {
			return true
		}
	}
}
