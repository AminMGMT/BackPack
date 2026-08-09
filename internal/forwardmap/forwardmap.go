package forwardmap

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	MaxPortsPerMapping = 1024
	MaxExpandedPorts   = 4096
)

// Mapping is one concrete Iran listen socket and its Kharej backend target.
type Mapping struct {
	Listen string
	Target string
}

type endpointRange struct {
	host         string
	lo, hi       int
	explicitHost bool
}

func parseEndpointRange(raw string) (endpointRange, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return endpointRange{}, fmt.Errorf("empty endpoint")
	}
	host, portText := "", raw
	if strings.Contains(raw, ":") {
		var err error
		host, portText, err = net.SplitHostPort(raw)
		if err != nil {
			return endpointRange{}, fmt.Errorf("invalid host:port %q (IPv6 addresses must be bracketed): %w", raw, err)
		}
	}
	parts := strings.Split(portText, "-")
	if len(parts) < 1 || len(parts) > 2 {
		return endpointRange{}, fmt.Errorf("invalid port range %q", portText)
	}
	parsePort := func(v string) (int, error) {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 1 || n > 65535 {
			return 0, fmt.Errorf("invalid port %q", v)
		}
		return n, nil
	}
	lo, err := parsePort(parts[0])
	if err != nil {
		return endpointRange{}, err
	}
	hi := lo
	if len(parts) == 2 {
		hi, err = parsePort(parts[1])
		if err != nil {
			return endpointRange{}, err
		}
		if hi < lo {
			return endpointRange{}, fmt.Errorf("port range %q ends before it starts", portText)
		}
	}
	return endpointRange{host: host, lo: lo, hi: hi, explicitHost: strings.Contains(raw, ":")}, nil
}

func (e endpointRange) len() int { return e.hi - e.lo + 1 }

func endpoint(e endpointRange, port int, listen bool) string {
	if e.explicitHost {
		return net.JoinHostPort(e.host, strconv.Itoa(port))
	}
	if listen {
		return ":" + strconv.Itoa(port)
	}
	return strconv.Itoa(port)
}

// Expand parses Backpack's existing mapping syntax. Ranges preserve offsets
// when the target is also a range. A single target intentionally fans a listen
// range into one backend, matching the historical reverse behaviour.
func Expand(specs []string) ([]Mapping, error) {
	var out []Mapping
	var listens []string
	for _, raw := range specs {
		left, right, hasTarget := strings.Cut(strings.TrimSpace(raw), "=")
		listenRange, err := parseEndpointRange(left)
		if err != nil {
			return nil, fmt.Errorf("mapping %q listen: %w", raw, err)
		}
		if listenRange.len() > MaxPortsPerMapping {
			return nil, fmt.Errorf("mapping %q expands beyond the %d-port mapping limit", raw, MaxPortsPerMapping)
		}
		if len(out)+listenRange.len() > MaxExpandedPorts {
			return nil, fmt.Errorf("mappings expand beyond the %d-port instance limit", MaxExpandedPorts)
		}

		var targets []endpointRange
		if !hasTarget {
			targets = []endpointRange{{lo: listenRange.lo, hi: listenRange.hi}}
		} else {
			for _, targetRaw := range strings.Split(right, "|") {
				target, err := parseEndpointRange(targetRaw)
				if err != nil {
					return nil, fmt.Errorf("mapping %q target: %w", raw, err)
				}
				if target.len() != 1 && target.len() != listenRange.len() {
					return nil, fmt.Errorf("mapping %q listen and target ranges must have equal length", raw)
				}
				targets = append(targets, target)
			}
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("mapping %q has no target", raw)
		}
		for offset := 0; offset < listenRange.len(); offset++ {
			targetParts := make([]string, 0, len(targets))
			for _, target := range targets {
				port := target.lo
				if target.len() > 1 {
					port += offset
				}
				targetParts = append(targetParts, endpoint(target, port, false))
			}
			listen := endpoint(listenRange, listenRange.lo+offset, true)
			for _, old := range listens {
				if listenOverlap(listen, old) {
					return nil, fmt.Errorf("listen endpoint %s overlaps %s", listen, old)
				}
			}
			listens = append(listens, listen)
			out = append(out, Mapping{
				Listen: listen,
				Target: strings.Join(targetParts, "|"),
			})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one ingress port mapping is required")
	}
	return out, nil
}

func listenOverlap(a, b string) bool {
	ah, ap, aerr := net.SplitHostPort(a)
	bh, bp, berr := net.SplitHostPort(b)
	if aerr != nil || berr != nil || ap != bp {
		return false
	}
	normalize := func(h string) string { return strings.Trim(strings.TrimSpace(h), "[]") }
	wild := func(h string) bool {
		switch normalize(h) {
		case "", "0.0.0.0", "::", "*":
			return true
		}
		return false
	}
	family := func(h string) int {
		h = normalize(h)
		if h == "" || h == "*" {
			return 0 // an unspecified Go listen address may claim both families
		}
		ip := net.ParseIP(h)
		if ip == nil {
			return 0 // unresolved names are conservatively treated as either family
		}
		if ip.To4() != nil {
			return 4
		}
		return 6
	}
	af, bf := family(ah), family(bh)
	if af != 0 && bf != 0 && af != bf {
		return false
	}
	return wild(ah) || wild(bh) || strings.EqualFold(ah, bh)
}
