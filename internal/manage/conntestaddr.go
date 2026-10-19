package manage

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
)

// ConnTestAddress is a candidate, not a claim that Kharej can reach it. Only
// the peer's coordinator check can establish that. Discovery does no HTTP
// requests: an outbound IP echo can name a different server behind a tunnel.
type ConnTestAddress struct {
	Host      string `json:"host"`
	Family    string `json:"family"`
	Interface string `json:"interface,omitempty"`
}

func ctPublicAddress(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() {
		return false
	}
	for _, prefix := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(prefix).Contains(a) {
			return false
		}
	}
	return true
}

// ConnTestAddresses lists every public address on an up interface, so a
// multihomed host does not silently test whichever address happened to be first.
// NAT and private test networks can still be entered explicitly.
func ConnTestAddresses() []ConnTestAddress {
	interfaces, _ := net.Interfaces()
	out := []ConnTestAddress{}
	seen := map[netip.Addr]bool{}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, raw := range addrs {
			prefix, err := netip.ParsePrefix(raw.String())
			if err != nil {
				continue
			}
			a := prefix.Addr().Unmap()
			if !ctPublicAddress(a) || seen[a] {
				continue
			}
			seen[a] = true
			out = append(out, ConnTestAddress{Host: a.String(), Family: ctFamily(a.String()), Interface: iface.Name})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Family != out[j].Family {
			return out[i].Family < out[j].Family
		}
		return out[i].Host < out[j].Host
	})
	return out
}

func ctFamily(host string) string {
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil && ip.To4() == nil {
		return "IPv6"
	}
	return "IPv4"
}

func ctBindHost(host string) string {
	if ctFamily(host) == "IPv6" {
		return "::"
	}
	return "0.0.0.0"
}

// Resolve once, before building links. A dual-stack DNS name must never let a
// supposedly IPv6 measurement fall back to IPv4 via Happy Eyeballs.
func ctResolveHost(ctx context.Context, host, family string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		if family == "" {
			return "", fmt.Errorf("this server's address is required")
		}
		return "", fmt.Errorf("an %s server address is required", family)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
			return "", fmt.Errorf("%q is not a usable server address", host)
		}
		if family != "" && ctFamily(host) != family {
			return "", fmt.Errorf("%q is not an %s address", host, family)
		}
		return ip.String(), nil
	}
	if strings.ContainsAny(host, ":/% \t\r\n") {
		return "", fmt.Errorf("%q is not an IP address or hostname", host)
	}
	network := "ip"
	if family == "IPv4" {
		network = "ip4"
	} else if family == "IPv6" {
		network = "ip6"
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, network, host)
	if err != nil {
		return "", fmt.Errorf("resolving %s for %s: %w", host, family, err)
	}
	// Preserve the old IPv4 default for an unqualified hostname.
	if family == "" {
		for _, ip := range ips {
			if ip.To4() != nil {
				return ip.String(), nil
			}
		}
	}
	if len(ips) > 0 {
		return ips[0].String(), nil
	}
	return "", fmt.Errorf("%s has no %s address", host, family)
}

// ConnTestHosts validates the selection shared by the panel and terminal.
// The legacy host-only request remains a single-path test.
func ConnTestHosts(ctx context.Context, mode, host, v4, v6 string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, connTestResolveWait)
	defer cancel()
	var inputs []ConnTestAddress
	switch mode {
	case "", "manual":
		inputs = []ConnTestAddress{{Host: host}}
	case "ipv4":
		inputs = []ConnTestAddress{{Host: v4, Family: "IPv4"}}
	case "ipv6":
		inputs = []ConnTestAddress{{Host: v6, Family: "IPv6"}}
	case "both":
		inputs = []ConnTestAddress{{Host: v4, Family: "IPv4"}, {Host: v6, Family: "IPv6"}}
	default:
		return nil, fmt.Errorf("path must be ipv4, ipv6, both or manual")
	}
	var out []string
	for _, input := range inputs {
		h, err := ctResolveHost(ctx, input.Host, input.Family)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}
