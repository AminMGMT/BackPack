package web

import (
	"fmt"
	"strconv"
	"strings"

	psnet "github.com/shirou/gopsutil/v4/net"
)

func portableSocketCount() (int, error) {
	connections, err := psnet.Connections("all")
	if err != nil {
		return 0, err
	}
	return len(connections), nil
}

func parseSocketCount(sockstat []byte) (int, error) {
	for _, line := range strings.Split(string(sockstat), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "sockets:" || fields[1] != "used" {
			continue
		}

		count, err := strconv.Atoi(fields[2])
		if err != nil || count < 0 {
			return 0, fmt.Errorf("invalid socket count %q", fields[2])
		}
		return count, nil
	}
	return 0, fmt.Errorf("socket count not found in sockstat")
}
