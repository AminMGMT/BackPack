// Package spooftest discovers which forged source IPs actually traverse the
// network, in each direction, so an operator can pick spoof addresses that work
// rather than guessing. It is the tool that turns the spoof transport from "set
// an IP and hope" into something provable.
//
// The IP-expansion logic here is adapted from ChromeSpoof (MIT, © 2026
// AminMGMT), which backpack's author also wrote.
package spooftest

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
)

// ExpandSpec turns one candidate specification into IPv4 addresses. Forms:
//
//	single   192.168.1.1
//	range    192.168.1.1-192.168.1.255
//	CIDR     192.168.1.0/24
func ExpandSpec(spec string) ([]net.IP, error) {
	spec = strings.TrimSpace(spec)
	switch {
	case strings.Contains(spec, "/"):
		return expandCIDR(spec)
	case strings.Contains(spec, "-"):
		return expandRange(spec)
	default:
		ip := net.ParseIP(spec)
		if ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("invalid IPv4 %q", spec)
		}
		return []net.IP{ip.To4()}, nil
	}
}

// ExpandList expands a comma-separated list of specs, or, when the argument is
// "@path", the specs in that file (one per line, blank and #comment lines
// skipped). The result is de-duplicated and preserves first-seen order.
func ExpandList(arg string) ([]net.IP, error) {
	if strings.HasPrefix(arg, "@") {
		return expandFile(strings.TrimPrefix(arg, "@"))
	}
	seen := make(map[uint32]struct{})
	var out []net.IP
	for _, spec := range strings.Split(arg, ",") {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		ips, err := ExpandSpec(spec)
		if err != nil {
			return nil, err
		}
		out = appendUnique(out, seen, ips)
	}
	return out, nil
}

func expandCIDR(spec string) ([]net.IP, error) {
	_, ipnet, err := net.ParseCIDR(spec)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", spec, err)
	}
	if ipnet.IP.To4() == nil {
		return nil, fmt.Errorf("only IPv4 CIDR supported: %q", spec)
	}
	start := binary.BigEndian.Uint32(ipnet.IP.To4())
	maskBits, _ := ipnet.Mask.Size()
	count := uint64(1) << uint(32-maskBits)
	out := make([]net.IP, 0, count)
	for i := uint64(0); i < count; i++ {
		out = append(out, u32ToIP(start+uint32(i)))
	}
	return out, nil
}

func expandRange(spec string) ([]net.IP, error) {
	parts := strings.SplitN(spec, "-", 2)
	a := net.ParseIP(strings.TrimSpace(parts[0]))
	b := net.ParseIP(strings.TrimSpace(parts[1]))
	if a == nil || b == nil || a.To4() == nil || b.To4() == nil {
		return nil, fmt.Errorf("invalid range %q", spec)
	}
	start := binary.BigEndian.Uint32(a.To4())
	end := binary.BigEndian.Uint32(b.To4())
	if end < start {
		start, end = end, start
	}
	out := make([]net.IP, 0, end-start+1)
	for v := start; ; v++ {
		out = append(out, u32ToIP(v))
		if v == end {
			break
		}
	}
	return out, nil
}

func expandFile(path string) ([]net.IP, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	seen := make(map[uint32]struct{})
	var out []net.IP
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ips, err := ExpandSpec(line)
		if err != nil {
			return nil, err
		}
		out = appendUnique(out, seen, ips)
	}
	return out, sc.Err()
}

func appendUnique(out []net.IP, seen map[uint32]struct{}, ips []net.IP) []net.IP {
	for _, ip := range ips {
		k := binary.BigEndian.Uint32(ip.To4())
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, ip)
	}
	return out
}

func u32ToIP(v uint32) net.IP {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return net.IP(b)
}
