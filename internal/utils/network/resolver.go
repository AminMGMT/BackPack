package network

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

func ResolveRemoteAddr(remoteAddr string) (int, string, error) {
	// A pipe list names several backends for health-checked load balancing
	// ("8443|127.0.0.1:8444"). Resolve each to a full host:port (so bare ports
	// still default to localhost) and pass the rebuilt list through for the pool
	// to split; the reported port is the first backend's, for metrics and logs.
	//
	// A pipe, not a comma: commas already separate whole port entries.
	if strings.Contains(remoteAddr, "|") {
		var resolved []string
		var firstPort int
		for i, part := range strings.Split(remoteAddr, "|") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			p, full, err := ResolveRemoteAddr(part)
			if err != nil {
				return 0, "", err
			}
			if i == 0 {
				firstPort = p
			}
			resolved = append(resolved, full)
		}
		return firstPort, strings.Join(resolved, "|"), nil
	}

	remoteAddr = strings.TrimSpace(remoteAddr)
	// A bare number means the historical loopback shorthand. Everything else
	// must be a valid host:port; net.SplitHostPort is required here because a
	// strings.Split on ':' corrupts bracketed IPv6 addresses.
	if !strings.Contains(remoteAddr, ":") {
		port, err := strconv.Atoi(remoteAddr)
		if err != nil {
			return 0, "", fmt.Errorf("invalid port format: %v", err)
		}
		if port < 1 || port > 65535 {
			return 0, "", fmt.Errorf("invalid port %d", port)
		}
		// Default to localhost if only the port is provided
		return port, fmt.Sprintf("127.0.0.1:%d", port), nil
	}
	host, portText, err := net.SplitHostPort(remoteAddr)
	if err != nil || strings.TrimSpace(host) == "" {
		return 0, "", fmt.Errorf("invalid remote address %q: %w", remoteAddr, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return 0, "", fmt.Errorf("invalid port format: %v", err)
	}
	if port < 1 || port > 65535 {
		return 0, "", fmt.Errorf("invalid port %d", port)
	}

	// Return the full resolved address
	return port, remoteAddr, nil
}
