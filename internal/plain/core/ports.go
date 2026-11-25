package core

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// ParsePortMappings reads the server's ports list and calls start for every
// listener it names, with the local address to bind and the remote address to
// send. Every plain server transport uses it:
//
//	"443"                   listen :443, send "443"
//	"443-600"               each port in the range, sending its own number
//	"4000=5000"             listen :4000, send "5000"
//	"443-600=1.1.1.1:5201"  each port in the range, sending "1.1.1.1:5201"
//	"127.0.0.2:443=5201"    listen 127.0.0.2:443, send "5201"
//
// A malformed entry is fatal.
func ParsePortMappings(ports []string, logger *logrus.Logger, start func(localAddr, remoteAddr string)) {
	for _, portMapping := range ports {
		parts := strings.Split(portMapping, "=")

		var localAddr, remoteAddr string

		if len(parts) == 1 {
			localPortOrRange := strings.TrimSpace(parts[0])
			remoteAddr = localPortOrRange

			if strings.Contains(localPortOrRange, "-") {
				rangeParts := strings.Split(localPortOrRange, "-")
				if len(rangeParts) != 2 {
					logger.Fatalf("invalid port range format: %s", localPortOrRange)
				}

				startPort, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
				if err != nil || startPort < 1 || startPort > 65535 {
					logger.Fatalf("invalid start port in range: %s", rangeParts[0])
				}

				endPort, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
				if err != nil || endPort < 1 || endPort > 65535 || endPort < startPort {
					logger.Fatalf("invalid end port in range: %s", rangeParts[1])
				}

				for port := startPort; port <= endPort; port++ {
					localAddr = fmt.Sprintf(":%d", port)
					go start(localAddr, strconv.Itoa(port))
					time.Sleep(1 * time.Millisecond) // for wide port ranges
				}
				continue
			} else {
				port, err := strconv.Atoi(localPortOrRange)
				if err != nil || port < 1 || port > 65535 {
					logger.Fatalf("invalid port format: %s", localPortOrRange)
				}
				localAddr = fmt.Sprintf(":%d", port)
			}
		} else if len(parts) == 2 {
			localPortOrRange := strings.TrimSpace(parts[0])
			remoteAddr = strings.TrimSpace(parts[1])

			if strings.Contains(localPortOrRange, "-") {
				rangeParts := strings.Split(localPortOrRange, "-")
				if len(rangeParts) != 2 {
					logger.Fatalf("invalid port range format: %s", localPortOrRange)
				}

				startPort, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
				if err != nil || startPort < 1 || startPort > 65535 {
					logger.Fatalf("invalid start port in range: %s", rangeParts[0])
				}

				endPort, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
				if err != nil || endPort < 1 || endPort > 65535 || endPort < startPort {
					logger.Fatalf("invalid end port in range: %s", rangeParts[1])
				}

				for port := startPort; port <= endPort; port++ {
					localAddr = fmt.Sprintf(":%d", port)
					go start(localAddr, remoteAddr)
					time.Sleep(1 * time.Millisecond) // for wide port ranges
				}
				continue
			} else {
				port, err := strconv.Atoi(localPortOrRange)
				if err == nil && port > 1 && port < 65535 { // format port=remoteAddress
					localAddr = fmt.Sprintf(":%d", port)
				} else {
					localAddr = localPortOrRange // format ip:port=remoteAddress
				}
			}
		} else {
			logger.Fatalf("invalid port mapping format: %s", portMapping)
		}
		go start(localAddr, remoteAddr)
	}
}
