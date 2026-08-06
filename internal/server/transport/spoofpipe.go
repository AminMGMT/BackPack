package transport

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/backpack/backpack/internal/utils/network"
	"github.com/sirupsen/logrus"
)

// SpoofPipeServer is the server half of the WireGuard-pipe mode: it relays
// datagrams between the forged-source spoof channel and the real WireGuard
// endpoint on this host. No KCP, no port forwarding — WireGuard carries
// everything itself.
type SpoofPipeServer struct {
	config *SpoofPipeConfig
	ctx    context.Context
	cancel context.CancelFunc
	logger *logrus.Logger
}

// SpoofPipeConfig is shared by both ends. Carrier and PeerIP describe the spoof
// channel; PipeAddr is this host's WireGuard UDP socket.
type SpoofPipeConfig struct {
	Token    string
	Carrier  network.SpoofCarrier
	PeerIP   string // the peer's real IPv4 (client's on the server, server's on the client)
	PipeAddr string // this host's WireGuard endpoint
	Retry    time.Duration
}

func NewSpoofPipeServer(parentCtx context.Context, config *SpoofPipeConfig, logger *logrus.Logger) *SpoofPipeServer {
	ctx, cancel := context.WithCancel(parentCtx)
	return &SpoofPipeServer{config: config, ctx: ctx, cancel: cancel, logger: logger}
}

func (s *SpoofPipeServer) Start() {
	retry := s.config.Retry
	if retry <= 0 {
		retry = 3 * time.Second
	}
	for {
		if s.ctx.Err() != nil {
			return
		}
		if err := s.runOnce(); err != nil && s.ctx.Err() == nil {
			s.logger.Errorf("spoof pipe: %v — retrying", err)
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}

func (s *SpoofPipeServer) Stop() { s.cancel() }

func (s *SpoofPipeServer) runOnce() error {
	peer := net.ParseIP(s.config.PeerIP)
	if peer == nil {
		return fmt.Errorf("spoof_peer_ip must be the client's real IPv4 address")
	}
	target, err := net.ResolveUDPAddr("udp", s.config.PipeAddr)
	if err != nil {
		return fmt.Errorf("resolving the WireGuard endpoint %q: %w", s.config.PipeAddr, err)
	}
	spoof, err := network.NewSpoofPacketConn(true, s.config.Token, s.config.Carrier, peer)
	if err != nil {
		return err
	}
	defer spoof.Close()
	tconn, err := net.DialUDP("udp", nil, target)
	if err != nil {
		return fmt.Errorf("dialling the WireGuard endpoint %q: %w", s.config.PipeAddr, err)
	}
	defer tconn.Close()

	// Unblock the relay's reads when the run is cancelled.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-s.ctx.Done():
		case <-stop:
		}
		spoof.Close()
		tconn.Close()
	}()

	s.logger.Infof("spoof pipe up: forwarding WireGuard to %s", s.config.PipeAddr)
	return network.SpoofPipeRelay(s.ctx, spoof, tconn, false)
}
