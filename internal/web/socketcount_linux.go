//go:build linux

package web

import "os"

func socketCount() (int, error) {
	sockstat, err := os.ReadFile("/proc/net/sockstat")
	if err == nil {
		if count, parseErr := parseSocketCount(sockstat); parseErr == nil {
			return count, nil
		}
	}

	// Preserve the old behaviour on unusual Linux environments without procfs.
	return portableSocketCount()
}
