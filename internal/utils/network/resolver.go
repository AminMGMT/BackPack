package network

import (
	"fmt"
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
	parts := strings.Split(remoteAddr, ":")
	var port int
	var err error

	// Handle cases where only the port is sent or host:port format
	if len(parts) < 2 {
		port, err = strconv.Atoi(parts[0])
		if err != nil {
			return 0, "", fmt.Errorf("invalid port format: %v", err)
		}
		// Default to localhost if only the port is provided
		return port, fmt.Sprintf("127.0.0.1:%d", port), nil
	}

	// If both host and port are provided
	port, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, "", fmt.Errorf("invalid port format: %v", err)
	}

	// Return the full resolved address
	return port, remoteAddr, nil
}
