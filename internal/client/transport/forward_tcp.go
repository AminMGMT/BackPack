package transport

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/forwardmap"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/web"
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

type forwardTCPPool struct {
	ctx    context.Context
	cancel context.CancelFunc
	size   int
	dial   func(context.Context) (net.Conn, error)
	ready  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newForwardTCPPool(parent context.Context, size int, dial func(context.Context) (net.Conn, error)) *forwardTCPPool {
	ctx, cancel := context.WithCancel(parent)
	return &forwardTCPPool{ctx: ctx, cancel: cancel, size: max(1, size), dial: dial, ready: make(chan net.Conn), closed: make(chan struct{})}
}

func (p *forwardTCPPool) Start() {
	for i := 0; i < p.size; i++ {
		go p.worker()
	}
}

func (p *forwardTCPPool) worker() {
	bo := newBackoff(100 * time.Millisecond)
	for p.ctx.Err() == nil {
		conn, err := p.dial(p.ctx)
		if err != nil {
			if !bo.Wait(p.ctx) {
				return
			}
			continue
		}
		// A successful connection means the outage is over. A later failure is
		// a new outage and should again recover quickly instead of inheriting a
		// 30-second backoff from an old one.
		bo = newBackoff(100 * time.Millisecond)
		select {
		case p.ready <- conn:
			// The connection was consumed. Refill this worker's one slot.
		case <-p.ctx.Done():
			conn.Close()
			return
		}
	}
}

func (p *forwardTCPPool) Get(ctx context.Context, timeout time.Duration) (net.Conn, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case conn := <-p.ready:
		if err := p.ctx.Err(); err != nil {
			conn.Close()
			return nil, err
		}
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.closed:
		return nil, fmt.Errorf("forward connection pool is closed")
	case <-timer.C:
		return nil, fmt.Errorf("timed out waiting for a forward data connection")
	}
}

func (p *forwardTCPPool) Close() {
	p.once.Do(func() {
		p.cancel()
		close(p.closed)
	})
}

func (c *TcpTransport) startForwardTCPIngress(ctx context.Context, usage *web.Usage, pool *forwardTCPPool) {
	mappings, err := expandForwardTCPMappings(c.config.Ports)
	if err != nil {
		c.logger.Errorf("invalid forward ingress mappings: %v", err)
		go c.restartGeneration(ctx)
		return
	}
	for _, mapping := range mappings {
		mapping := mapping
		go c.runForwardTCPListener(ctx, usage, pool, mapping)
	}
}

func (c *TcpTransport) runForwardTCPListener(ctx context.Context, usage *web.Usage, pool *forwardTCPPool, mapping forwardTCPMapping) {
	listener, err := net.Listen("tcp", mapping.listen)
	if err != nil {
		c.logger.Errorf("failed to listen on forward ingress %s: %v", mapping.listen, err)
		go c.restartGeneration(ctx)
		return
	}
	defer listener.Close()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	c.logger.Infof("forward ingress listening on %s -> Kharej %s", listener.Addr(), mapping.target)

	for {
		local, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
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
		go c.handleForwardTCPIngress(ctx, usage, pool, local, mapping.target)
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

func (c *TcpTransport) handleForwardTCPIngress(ctx context.Context, usage *web.Usage, pool *forwardTCPPool, local net.Conn, target string) {
	defer atomic.AddInt32(&c.loadConnections, -1)
	local = c.forwardBandwidth.wrap(local)
	tunnel, err := c.openForwardTCP(ctx, pool, target)
	if err != nil {
		c.logger.Warnf("could not open forward channel for %s: %v", target, err)
		local.Close()
		return
	}
	port := 0
	if tcpAddr, ok := local.LocalAddr().(*net.TCPAddr); ok {
		port = tcpAddr.Port
	}
	handlers.TCPConnectionHandler(ctx, c.config.ProxyProtocol, local, metrics.CountedConn(tunnel), c.logger, usage, port, c.config.Sniffer)
}
