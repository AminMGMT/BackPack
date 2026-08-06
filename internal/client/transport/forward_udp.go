package transport

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
)

type clientForwardUDPFlow struct {
	client  *net.UDPAddr
	payload chan []byte
}

func (c *UdpTransport) startForwardUDPIngress() {
	mappings, err := expandForwardTCPMappings(c.config.Ports)
	if err != nil {
		c.logger.Errorf("invalid forward UDP mappings: %v", err)
		go c.Restart()
		return
	}
	for _, mapping := range mappings {
		mapping := mapping
		go c.runForwardUDPListener(mapping)
	}
}

func (c *UdpTransport) runForwardUDPListener(mapping forwardTCPMapping) {
	addr, err := net.ResolveUDPAddr("udp", mapping.listen)
	if err != nil {
		c.logger.Errorf("invalid UDP ingress %s: %v", mapping.listen, err)
		return
	}
	listener, err := net.ListenUDP("udp", addr)
	if err != nil {
		c.logger.Errorf("failed to listen on UDP ingress %s: %v", mapping.listen, err)
		go c.Restart()
		return
	}
	c.applyBuffers(listener)
	defer listener.Close()
	go func() { <-c.state.Ctx().Done(); listener.Close() }()
	c.logger.Infof("forward UDP ingress listening on %s -> Kharej %s", listener.LocalAddr(), mapping.target)

	flows := map[string]*clientForwardUDPFlow{}
	var mu sync.Mutex
	buf := make([]byte, 64*1024)
	for {
		n, clientAddr, err := listener.ReadFromUDP(buf)
		if err != nil {
			if c.state.Ctx().Err() != nil {
				return
			}
			continue
		}
		key := clientAddr.String()
		mu.Lock()
		flow := flows[key]
		if flow == nil {
			if !acquireForwardConnection(&c.forwardActive, c.config.MaxConnections) {
				mu.Unlock()
				continue
			}
			flow = &clientForwardUDPFlow{client: clientAddr, payload: make(chan []byte, 1024)}
			flows[key] = flow
			go c.runForwardUDPFlow(listener, mapping.target, key, flow, &mu, flows)
		}
		select {
		case flow.payload <- append([]byte(nil), buf[:n]...):
		default:
			c.logger.Warnf("forward UDP flow %s queue is full; dropping packet", key)
		}
		mu.Unlock()
	}
}

func (c *UdpTransport) runForwardUDPFlow(listener *net.UDPConn, target, key string, flow *clientForwardUDPFlow, mu *sync.Mutex, flows map[string]*clientForwardUDPFlow) {
	defer func() {
		atomic.AddInt32(&c.forwardActive, -1)
		mu.Lock()
		if flows[key] == flow {
			delete(flows, key)
			close(flow.payload)
		}
		mu.Unlock()
	}()
	remote, err := net.ResolveUDPAddr("udp", c.config.Endpoints.Next())
	if err != nil {
		return
	}
	tunnel, err := net.DialUDP("udp", nil, remote)
	if err != nil {
		return
	}
	c.applyBuffers(tunnel)
	defer tunnel.Close()
	announcement, err := utils.EncodeForwardUDP(c.config.Token, target)
	if err != nil {
		return
	}
	if _, err := tunnel.Write(announcement); err != nil {
		return
	}
	_ = tunnel.SetReadDeadline(time.Now().Add(c.config.DialTimeOut))
	ack := []byte{0}
	if _, err := tunnel.Read(ack); err != nil || ack[0] != utils.SG_ForwardOK {
		return
	}
	_ = tunnel.SetReadDeadline(time.Time{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-c.state.Ctx().Done():
				return
			case payload, ok := <-flow.payload:
				if !ok {
					return
				}
				c.forwardBandwidth.wait(len(payload))
				_ = tunnel.SetWriteDeadline(time.Now().Add(60 * time.Second))
				if _, err := tunnel.Write(payload); err != nil {
					return
				}
				metrics.AddBytes(0, uint64(len(payload)))
			}
		}
	}()

	buf := make([]byte, 64*1024)
	for {
		_ = tunnel.SetReadDeadline(time.Now().Add(60 * time.Second))
		n, err := tunnel.Read(buf)
		if err != nil {
			return
		}
		c.forwardBandwidth.wait(n)
		if _, err := listener.WriteToUDP(buf[:n], flow.client); err != nil {
			return
		}
		metrics.AddBytes(uint64(n), 0)
		select {
		case <-done:
			return
		default:
		}
	}
}
