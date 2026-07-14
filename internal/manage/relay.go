package manage

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/backpack/backpack/internal/app"
)

// EnsureSocksPort makes sure the given server tunnel exposes a port that maps to
// the peer's local SOCKS5 proxy (127.0.0.1:SocksInternalPort), so this node can
// reach the internet through the peer. It returns the exposed port, adding the
// mapping (and restarting the tunnel) if it isn't already present.
func EnsureSocksPort(name string) (int, error) {
	spec, err := loadServerSpec(name)
	if err != nil {
		return 0, err
	}
	suffix := fmt.Sprintf("=127.0.0.1:%d", app.SocksInternalPort)
	for _, p := range spec.Ports {
		if strings.HasSuffix(p, suffix) {
			if n, e := strconv.Atoi(strings.TrimSuffix(p, suffix)); e == nil {
				return n, nil
			}
		}
	}
	r := randomHighPort()
	spec.Ports = append(spec.Ports, fmt.Sprintf("%d%s", r, suffix))
	if _, err := spec.Save(); err != nil {
		return 0, err
	}
	// Restart so the new forwarded port takes effect.
	RestartService(app.ServiceName(name))
	return r, nil
}

// randomHighPort returns a random ephemeral-ish port in [20000, 60000).
func randomHighPort() int {
	n, err := rand.Int(rand.Reader, big.NewInt(40000))
	if err != nil {
		return 45678
	}
	return 20000 + int(n.Int64())
}
