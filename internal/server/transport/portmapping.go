package transport

import (
	"fmt"
	"strconv"
	"strings"
)

// mappingListenAddress converts the numeric shorthand used by forwarded-port
// mappings into a wildcard listen address. Non-numeric values are already full
// listen addresses and are returned unchanged.
func mappingListenAddress(value string) string {
	value = strings.TrimSpace(value)
	port, err := strconv.Atoi(value)
	if err == nil && port >= 1 && port <= 65535 {
		return fmt.Sprintf(":%d", port)
	}
	return value
}
