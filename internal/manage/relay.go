package manage

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"

	"github.com/backpack/backpack/internal/app"
)

// EnsureSocksPort makes sure the given server tunnel exposes a port that maps
// to the peer's local SOCKS5 proxy, so this node can reach the internet through
// the peer. It returns the exposed port, adding the mapping (and restarting the
// tunnel) if it isn't already present.
//
// The peer-side port is derived from the tunnel token rather than fixed at
// 1080. Both ends know the token, so both arrive at the same number without
// exchanging anything — and it avoids the well-known SOCKS port, which is
// frequently already in use and used to make the relay fail silently.
func EnsureSocksPort(name string) (int, error) {
	spec, err := loadServerSpec(name)
	if err != nil {
		return 0, err
	}

	// An existing mapping is kept whatever it points at, including the old
	// fixed port: rewriting it would break a working relay for no reason, and
	// the peer still listens on both.
	for _, p := range spec.Ports {
		if isBotRelayPort(p, spec.Token) {
			if n, e := strconv.Atoi(strings.TrimSpace(strings.SplitN(p, "=", 2)[0])); e == nil {
				return n, nil
			}
		}
	}

	peerPort := app.SocksPortForToken(spec.Token)
	exposed := randomHighPort()
	spec.Ports = append(spec.Ports, fmt.Sprintf("%d=127.0.0.1:%d", exposed, peerPort))
	if _, err := spec.Save(); err != nil {
		return 0, err
	}
	// Restart so the new forwarded port takes effect.
	RestartService(app.ServiceName(name))
	return exposed, nil
}

// randomHighPort returns a random ephemeral-ish port in [20000, 60000).
func randomHighPort() int {
	// Tried a few times so the port is one nothing else already holds.
	//
	// Picking blindly could land on a service that is already listening. The
	// tunnel's own listener then fails to bind while the other service happily
	// answers — so the relay looks like it is working and replies with
	// something that is not Telegram at all.
	for i := 0; i < 20; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(40000))
		if err != nil {
			break
		}
		port := 20000 + int(n.Int64())
		if portFree(port) {
			return port
		}
	}
	return 45678
}

// portFree reports whether a TCP port can be bound right now.
func portFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}
