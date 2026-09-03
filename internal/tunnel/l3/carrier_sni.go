package l3

import (
	"net"
	"sync"

	"github.com/backpack/backpack/internal/snispoof"
)

// The SNI-spoofing carrier.
//
// It is the pck carrier — raw TCP segments this process builds itself — with
// one extra segment sent at the start of the flow: a TLS ClientHello naming a
// domain the path is known to allow. A filtering box that classifies by the
// server name reads it, decides the flow is permitted, and stops looking; the
// tunnel's own segments follow on the same five-tuple.
//
// The technique is patterniha's, by way of therealaleph/sni-spoofing-rust.
//
// Why this is a wrapper and not a change to pck.
//
// The pck carrier is the thing every direct tunnel over a filtered TCP path
// depends on, and its framing took real work to get right. Nothing here edits
// it: the carrier is opened exactly as it always was and used through its own
// interface. If the hello turns out to be wrong for a route, the tunnel under
// it is the same tunnel it would have been.
//
// Why the hello is an ordinary in-window segment.
//
// The original technique gives the fake hello a sequence number behind the
// window, so the real server it is addressed to discards it as data it has
// already taken. That is necessary when the far end is somebody else's TLS
// server. Here the far end is the other half of this tunnel, so there is a
// better answer than hoping it discards the segment: it is told to. The
// receiving side drops the hello before the tunnel ever sees it, and nothing
// has to be smuggled past a stranger's stack.
//
// How the far end recognises it.
//
// By shape: a TLS record header, a client_hello, and exactly the length this
// package emits. The tunnel's own datagrams are sealed, so their first bytes
// are indistinguishable from random — the chance of one matching all three is
// negligible, and the cost if one ever did is a single dropped datagram on a
// carrier that is unreliable by design and whose layer above already handles
// loss. That is a better trade than carrying a marker in every datagram.

// openSNI builds the pck carrier and puts a hello in front of it.
func openSNI(cfg Config) (DatagramCarrier, net.Addr, error) {
	below, peer, err := openPck(cfg)
	if err != nil {
		return nil, nil, err
	}
	hello, err := snispoof.BuildHello(snispoof.DomainOr(cfg.SNIDomain))
	if err != nil {
		below.Close()
		return nil, nil, err
	}
	return &sniCarrier{DatagramCarrier: below, hello: hello}, peer, nil
}

type sniCarrier struct {
	DatagramCarrier
	hello []byte
	once  sync.Once
}

func (c *sniCarrier) CarrierName() string { return CarrierSNI }

// Overhead is the carrier's own. The hello is one segment at the start of the
// flow, not a per-datagram cost, so it does not come out of the MTU.
func (c *sniCarrier) Overhead() int { return c.DatagramCarrier.Overhead() }

// WriteTo sends the hello once, ahead of the first datagram that goes anywhere.
//
// At the start of the flow because that is where a classifier is looking: the
// decision about what a connection is gets made on its first packets, and a
// hello sent later arrives after the box has already made up its mind.
func (c *sniCarrier) WriteTo(p []byte, addr net.Addr) (int, error) {
	c.once.Do(func() { _, _ = c.DatagramCarrier.WriteTo(c.hello, addr) })
	return c.DatagramCarrier.WriteTo(p, addr)
}

// ReadFrom hands up everything except the peer's hello.
func (c *sniCarrier) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.DatagramCarrier.ReadFrom(p)
		if err != nil {
			return n, addr, err
		}
		if snispoof.LooksLikeHello(p[:n]) {
			continue
		}
		return n, addr, nil
	}
}
