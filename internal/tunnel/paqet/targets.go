package paqet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// What a client may ask the server to dial.
//
// paqet's server dials whatever target a stream names: whoever holds the key
// gets a TCP and UDP proxy out of the server, to the internet, to its private
// network and to the services it binds to loopback only. Backpack's kharej
// file knows nothing of the Iran side's ports, so it cannot list them either.
//
// What it can do is keep the boundary the previous PCK had. That engine gave
// the other end an address on a TUN device, and with it the kharej host's own
// services and nothing beyond them unless its operator set up forwarding and
// NAT there. So by default a target must be this host: a loopback address, an
// unspecified one, or one of its own interface addresses (a mapping that names
// the kharej's public address explicitly keeps working). Anything else — a
// cloud metadata address, a neighbour on the private network, the internet —
// is refused with a status the client reports by name, unless the kharej's
// own file sets pck_remote_targets = true. The decision belongs to the server
// that would make the connection, which is why it is a key of that file.
//
// A name is resolved here and the address that passed is the one dialled, so
// a name cannot pass the check as one address and be dialled as another.
//
// This also bounds what a weak cipher exposes: with pck_kcp_block = "none",
// anyone who can reach the port can open streams, and by default they reach
// only what the Iran server could reach through the old TUN anyway.

var errForbiddenTarget = errors.New("pck: target is not this server")

// RefusedByPolicy reports whether err is the other server's refusal to dial a
// target that is not its own (statusForbidden), as opposed to the target not
// answering: the forwarder words the two differently.
func RefusedByPolicy(err error) bool {
	var se statusError
	return errors.As(err, &se) && byte(se) == statusForbidden
}

// targetPolicy is the server's rule for targets.
type targetPolicy struct {
	// remote lets the server dial any target (pck_remote_targets).
	remote bool

	// Test hooks; nil uses the host's.
	addrs  func() ([]net.Addr, error)
	lookup func(ctx context.Context, host string) ([]net.IP, error)

	mu    sync.Mutex
	local []net.IP
	at    time.Time
}

// localAddrsTTL is how long the host's own addresses are trusted before they
// are read again: an address added at run time is seen within it.
const localAddrsTTL = 30 * time.Second

// lookupTimeout bounds resolving a target named by host name.
const lookupTimeout = 5 * time.Second

// check returns the address to dial for target, or why it may not be.
func (p *targetPolicy) check(ctx context.Context, target Addr) (Addr, error) {
	if p.remote {
		return target, nil
	}
	host := strings.TrimSpace(target.Host)
	if host == "" {
		return Addr{Host: loopbackIPv4, Port: target.Port}, nil
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		lookup := p.lookup
		if lookup == nil {
			lookup = func(ctx context.Context, host string) ([]net.IP, error) {
				return net.DefaultResolver.LookupIP(ctx, "ip", host)
			}
		}
		lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		var err error
		ips, err = lookup(lctx, host)
		cancel()
		if err != nil {
			return Addr{}, err
		}
	}
	for _, ip := range ips {
		if p.isThisHost(ip) {
			return Addr{Host: ip.String(), Port: target.Port}, nil
		}
	}
	return Addr{}, fmt.Errorf("%w: %s is not an address of this server, and pck_remote_targets is off", errForbiddenTarget, host)
}

const loopbackIPv4 = "127.0.0.1"

func (p *targetPolicy) isThisHost(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.local == nil || time.Since(p.at) > localAddrsTTL {
		addrs := p.addrs
		if addrs == nil {
			addrs = net.InterfaceAddrs
		}
		list, err := addrs()
		if err == nil {
			p.local = p.local[:0]
			for _, a := range list {
				if n, ok := a.(*net.IPNet); ok {
					p.local = append(p.local, n.IP)
				}
			}
			p.at = time.Now()
		}
	}
	for _, l := range p.local {
		if l.Equal(ip) {
			return true
		}
	}
	return false
}

// maxServerStreams bounds the forwarded connections open through one server at
// once. It is far above what a busy Iran server forwards (the setups this was
// sized against carry a few thousand) and well below what pins a small VPS's
// memory: each costs two sockets' buffers, a few goroutines and smux's
// per-stream window. Beyond it a request is answered "busy", not dialled.
const maxServerStreams = 65536

// streamLimit counts the forwarded connections open through a server.
type streamLimit struct {
	max int64
	n   atomic.Int64
}

func (l *streamLimit) acquire() (release func(), ok bool) {
	if l.n.Add(1) > l.max {
		l.n.Add(-1)
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(func() { l.n.Add(-1) }) }, true
}
