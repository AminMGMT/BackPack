package transport

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/backpack/backpack/internal/utils/network"
	"github.com/sirupsen/logrus"
)

// SpoofPipeClient is the client half of the WireGuard-pipe mode: it listens on
// the local WireGuard endpoint and relays its datagrams over the forged-source
// spoof channel to the server. WireGuard's `endpoint` points at PipeAddr.
type SpoofPipeClient struct {
	config *SpoofPipeConfig
	ctx    context.Context
	cancel context.CancelFunc
	logger *logrus.Logger
}

// SpoofPipeConfig is shared by both ends. Carrier describes the spoof channel;
// ServerIP is the server's real IPv4 (the routing destination); PipeAddr is the
// local UDP socket WireGuard sends to.
type SpoofPipeConfig struct {
	Token    string
	Carrier  network.SpoofCarrier
	ServerIP string // the server's real IPv4, resolved from the remote address
	PipeAddr string // local WireGuard endpoint to listen on
	Retry    time.Duration
}

func NewSpoofPipeClient(parentCtx context.Context, config *SpoofPipeConfig, logger *logrus.Logger) *SpoofPipeClient {
	ctx, cancel := context.WithCancel(parentCtx)
	return &SpoofPipeClient{config: config, ctx: ctx, cancel: cancel, logger: logger}
}

func (c *SpoofPipeClient) Start() {
	retry := c.config.Retry
	if retry <= 0 {
		retry = 3 * time.Second
	}
	for {
		if c.ctx.Err() != nil {
			return
		}
		if err := c.runOnce(); err != nil && c.ctx.Err() == nil {
			c.logger.Errorf("spoof pipe: %v — retrying", err)
		}
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}

func (c *SpoofPipeClient) Stop() { c.cancel() }

func (c *SpoofPipeClient) runOnce() error {
	sa, err := net.ResolveIPAddr("ip4", c.config.ServerIP)
	if err != nil || sa.IP.To4() == nil {
		return fmt.Errorf("could not resolve the server's real IPv4 address %q: %w", c.config.ServerIP, err)
	}
	server := sa.IP
	laddr, err := net.ResolveUDPAddr("udp", c.config.PipeAddr)
	if err != nil {
		return fmt.Errorf("resolving the local WireGuard endpoint %q: %w", c.config.PipeAddr, err)
	}
	local, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return fmt.Errorf("listening on the local WireGuard endpoint %q: %w", c.config.PipeAddr, err)
	}
	defer local.Close()
	// Match the carrier's socket buffers on the WireGuard side too, so a burst
	// coming off the tunnel is not dropped here on its way to WireGuard.
	network.SizePipeUDP(local, c.config.Carrier.SockBuf)
	spoof, err := network.NewSpoofPacketConn(false, c.config.Token, c.config.Carrier, server)
	if err != nil {
		return err
	}
	defer spoof.Close()

	// Unblock the relay's reads when the run is cancelled.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-c.ctx.Done():
		case <-stop:
		}
		spoof.Close()
		local.Close()
	}()

	c.logger.Infof("spoof pipe up: point WireGuard's endpoint at %s", c.config.PipeAddr)
	return network.SpoofPipeRelay(c.ctx, spoof, local, true)
}
