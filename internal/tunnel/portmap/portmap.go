// Package portmap parses the forwarded-port syntax the tunnels share.
//
// It is the syntax the reverse tunnel has always used, kept identical here so
// that an operator moving a config between tunnel kinds does not have to learn
// a second one:
//
//	"443"                          :443          -> default:443
//	"443=8443"                     :443          -> default:8443
//	"443=10.0.0.5:8443"            :443          -> 10.0.0.5:8443
//	"127.0.0.1:443=8443"           127.0.0.1:443 -> default:8443
//	"10000-10009"                  a range, each port to the same port
//	"10000-10009=20000-20009"      a range, preserving the offset
//	"443=10.0.0.1:80|10.0.0.2:80"  two backends, tried in turn
//
// What a target with no host of its own means is the caller's to decide, and
// the two callers mean different things by it. For a layer-3 tunnel it is the
// peer's address on the tunnel, which the kernel routes over the interface.
// For a direct layer-4 tunnel it is the loopback of the machine at the far
// end, where the real service listens. Both are the case that mapping almost
// always wants, which is why neither has to be written out.
package portmap

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	// MaxPortsPerMapping bounds one range, so a typo in a config cannot ask
	// for sixty thousand listeners.
	MaxPortsPerMapping = 1024

	// MaxExpandedPorts bounds the whole set.
	MaxExpandedPorts = 4096
)

// Mapping is one listening socket and the backends behind it.
type Mapping struct {
	// Listen is the local address to bind, as net.Listen takes it.
	Listen string

	// Targets are the addresses to dial, in preference order. More than one
	// means the mapping load-balances: each new connection starts at the next
	// member, and a member that refuses is skipped rather than failing the
	// connection.
	Targets []string
}

// String renders the mapping the way the log should show it.
func (m Mapping) String() string {
	return m.Listen + " -> " + strings.Join(m.Targets, "|")
}

// portRange is one endpoint's host and span of ports.
type portRange struct {
	host     string
	lo, hi   int
	hasHost  bool
	explicit bool // the text carried a host, even an empty one before the colon
}

func (r portRange) count() int { return r.hi - r.lo + 1 }

// parsePortRange reads "443", "10000-10009", "127.0.0.1:443" or
// "[::1]:10000-10009".
func parsePortRange(text string) (portRange, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return portRange{}, fmt.Errorf("empty endpoint")
	}

	host, ports := "", text
	// A bracketed literal is unambiguous; otherwise a colon separates a host
	// from the port, and a bare IPv6 address without brackets is refused
	// rather than guessed at.
	if strings.HasPrefix(text, "[") {
		end := strings.Index(text, "]")
		if end < 0 || end+1 >= len(text) || text[end+1] != ':' {
			return portRange{}, fmt.Errorf("%q is not a bracketed [host]:port", text)
		}
		host, ports = text[1:end], text[end+2:]
	} else if idx := strings.LastIndex(text, ":"); idx >= 0 {
		if strings.Count(text, ":") > 1 {
			return portRange{}, fmt.Errorf("%q looks like an IPv6 address; write it as [address]:port", text)
		}
		host, ports = text[:idx], text[idx+1:]
	}

	lo, hi, err := parsePortSpan(ports)
	if err != nil {
		return portRange{}, err
	}
	return portRange{
		host:     host,
		lo:       lo,
		hi:       hi,
		hasHost:  host != "",
		explicit: host != "" || strings.Contains(text, ":"),
	}, nil
}

func parsePortSpan(text string) (int, int, error) {
	parsePort := func(s string) (int, error) {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 1 || n > 65535 {
			return 0, fmt.Errorf("%q is not a port between 1 and 65535", s)
		}
		return n, nil
	}
	lo, hi, found := strings.Cut(text, "-")
	start, err := parsePort(lo)
	if err != nil {
		return 0, 0, err
	}
	if !found {
		return start, start, nil
	}
	end, err := parsePort(hi)
	if err != nil {
		return 0, 0, err
	}
	if end < start {
		return 0, 0, fmt.Errorf("range %q ends before it starts", text)
	}
	if end-start+1 > MaxPortsPerMapping {
		return 0, 0, fmt.Errorf("range %q asks for more than %d ports", text, MaxPortsPerMapping)
	}
	return start, end, nil
}

// Expand turns the configured mapping strings into concrete listeners and
// targets. defaultHost is what a target with no host of its own means.
func Expand(specs []string, defaultHost string) ([]Mapping, error) {
	var out []Mapping

	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}

		left, right, hasTarget := strings.Cut(spec, "=")
		listen, err := parsePortRange(left)
		if err != nil {
			return nil, fmt.Errorf("mapping %q: %w", spec, err)
		}
		if !hasTarget {
			// "443" forwards to the same port on the peer.
			right = left
			// ...but the listen side's host, if any, is local and must not
			// become the target's.
			right = strconv.Itoa(listen.lo)
			if listen.count() > 1 {
				right = strconv.Itoa(listen.lo) + "-" + strconv.Itoa(listen.hi)
			}
		}

		mappings, err := expandOne(spec, listen, right, defaultHost)
		if err != nil {
			return nil, err
		}
		out = append(out, mappings...)
		if len(out) > MaxExpandedPorts {
			return nil, fmt.Errorf("the mappings expand to more than %d ports", MaxExpandedPorts)
		}
	}
	return out, nil
}

// expandOne handles a single spec once its listen side is parsed.
func expandOne(spec string, listen portRange, target, defaultHost string) ([]Mapping, error) {
	alternatives := strings.Split(target, "|")

	// Several backends and a range together have no sensible reading — which
	// backend does port 10005 belong to? — so it is refused rather than
	// guessed.
	if len(alternatives) > 1 && listen.count() > 1 {
		return nil, fmt.Errorf("mapping %q: a port range cannot have several backends", spec)
	}

	if len(alternatives) > 1 {
		targets := make([]string, 0, len(alternatives))
		for _, alt := range alternatives {
			r, err := parsePortRange(alt)
			if err != nil {
				return nil, fmt.Errorf("mapping %q: %w", spec, err)
			}
			if r.count() > 1 {
				return nil, fmt.Errorf("mapping %q: a backend cannot be a port range", spec)
			}
			addr, err := targetAddr(r, r.lo, defaultHost)
			if err != nil {
				return nil, fmt.Errorf("mapping %q: %w", spec, err)
			}
			targets = append(targets, addr)
		}
		return []Mapping{{Listen: listenAddr(listen, listen.lo), Targets: targets}}, nil
	}

	to, err := parsePortRange(target)
	if err != nil {
		return nil, fmt.Errorf("mapping %q: %w", spec, err)
	}
	if to.count() > 1 && to.count() != listen.count() {
		return nil, fmt.Errorf("mapping %q: %d ports on the left and %d on the right",
			spec, listen.count(), to.count())
	}

	out := make([]Mapping, 0, listen.count())
	for i := 0; i < listen.count(); i++ {
		port := to.lo
		if to.count() > 1 {
			port = to.lo + i // ranges preserve the offset
		}
		addr, err := targetAddr(to, port, defaultHost)
		if err != nil {
			return nil, fmt.Errorf("mapping %q: %w", spec, err)
		}
		out = append(out, Mapping{
			Listen:  listenAddr(listen, listen.lo+i),
			Targets: []string{addr},
		})
	}
	return out, nil
}

// listenAddr renders one local bind address. No host means every interface,
// which is what a bare port has always meant here.
func listenAddr(r portRange, port int) string {
	if r.hasHost {
		return net.JoinHostPort(r.host, strconv.Itoa(port))
	}
	return ":" + strconv.Itoa(port)
}

// targetAddr renders one backend address, defaulting the host to the peer's
// address on the tunnel.
func targetAddr(r portRange, port int, defaultHost string) (string, error) {
	host := r.host
	if host == "" {
		if defaultHost == "" {
			return "", fmt.Errorf("the target has no host and no default was given")
		}
		host = defaultHost
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}
