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

	// Split the address into host and port
	// A port on its own means this machine's loopback.
	if !strings.Contains(remoteAddr, ":") {
		port, err := strconv.Atoi(remoteAddr)
		if err != nil {
			return 0, "", fmt.Errorf("invalid port format: %v", err)
		}
		return port, fmt.Sprintf("127.0.0.1:%d", port), nil
	}

	// host:port, including a bracketed IPv6 host. This used to split on ':'
	// and read the second field as the port, so "[2001:db8::1]:443" was
	// refused as a bad port on every connection to it.
	_, portStr, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return 0, "", fmt.Errorf("invalid port format: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, "", fmt.Errorf("invalid port format: %v", err)
	}
	return port, remoteAddr, nil
}
