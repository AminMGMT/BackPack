package transport

import (
	"net"
	"sync/atomic"

	"github.com/backpack/backpack/internal/forwardmap"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils/handlers"
)

type forwardTCPMapping struct {
	listen string
	target string
}

// expandForwardTCPMappings normalises the long-standing Backpack port syntax
// for the dialling Iran edge. Ranges preserve their offset when both sides are
// ranges; a single target intentionally fans the whole listen range into that
// one backend, matching the historical server-side behaviour.
func expandForwardTCPMappings(specs []string) ([]forwardTCPMapping, error) {
	expanded, err := forwardmap.Expand(specs)
	if err != nil {
		return nil, err
	}
	out := make([]forwardTCPMapping, len(expanded))
	for i, mapping := range expanded {
		out[i] = forwardTCPMapping{listen: mapping.Listen, target: mapping.Target}
	}
	return out, nil
}

func (c *TcpTransport) startForwardTCPIngress() {
	mappings, err := expandForwardTCPMappings(c.config.Ports)
	if err != nil {
		c.logger.Errorf("invalid forward ingress mappings: %v", err)
		go c.Restart()
		return
	}
	for _, mapping := range mappings {
		mapping := mapping
		go c.runForwardTCPListener(mapping)
	}
}

func (c *TcpTransport) runForwardTCPListener(mapping forwardTCPMapping) {
	listener, err := net.Listen("tcp", mapping.listen)
	if err != nil {
		c.logger.Errorf("failed to listen on forward ingress %s: %v", mapping.listen, err)
		go c.Restart()
		return
	}
	defer listener.Close()
	go func() {
		<-c.state.Ctx().Done()
		_ = listener.Close()
	}()
	c.logger.Infof("forward ingress listening on %s -> Kharej %s", listener.Addr(), mapping.target)

	for {
		local, err := listener.Accept()
		if err != nil {
			if c.state.Ctx().Err() != nil {
				return
			}
			c.logger.Warnf("forward ingress accept on %s failed: %v", mapping.listen, err)
			continue
		}
		if !c.acquireForwardSlot() {
			c.logger.Warnf("forward connection limit reached, refusing %s", local.RemoteAddr())
			local.Close()
			continue
		}
		go c.handleForwardTCPIngress(local, mapping.target)
	}
}

func (c *TcpTransport) acquireForwardSlot() bool {
	if c.config.MaxConnections <= 0 {
		atomic.AddInt32(&c.loadConnections, 1)
		return true
	}
	for {
		current := atomic.LoadInt32(&c.loadConnections)
		if int(current) >= c.config.MaxConnections {
			return false
		}
		if atomic.CompareAndSwapInt32(&c.loadConnections, current, current+1) {
			return true
		}
	}
}

func (c *TcpTransport) handleForwardTCPIngress(local net.Conn, target string) {
	defer atomic.AddInt32(&c.loadConnections, -1)
	local = c.forwardBandwidth.wrap(local)
	tunnel, err := c.openForwardTCP(target)
	if err != nil {
		c.logger.Warnf("could not open forward channel for %s: %v", target, err)
		local.Close()
		return
	}
	port := 0
	if tcpAddr, ok := local.LocalAddr().(*net.TCPAddr); ok {
		port = tcpAddr.Port
	}
	handlers.TCPConnectionHandler(c.state.Ctx(), c.config.ProxyProtocol, local, metrics.CountedConn(tunnel), c.logger, c.state.Usage(), port, c.config.Sniffer)
}
