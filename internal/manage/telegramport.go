package manage

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// Reaching Telegram through a tunnel, without a proxy.
//
// The original design sent the bot through a SOCKS5 proxy running on the peer:
// the Iran server exposed a port mapped to the peer's loopback, and a proxy
// there made the outbound connection. It worked, but it needed a second moving
// part on the far machine, and when that part was missing — no monitor service,
// a port already taken — the failure surfaced as a bare "EOF" on the near side.
//
// A tunnel already forwards a port to an arbitrary destination, so it can
// simply forward to api.telegram.org:443 directly. No proxy, nothing to install
// on the peer, nothing to keep running. The bot connects to a local port and
// the bytes come out of the peer's network card.
//
// TLS is unaffected and, if anything, safer: the request is still
// https://api.telegram.org/..., so the certificate is still verified against
// that name. The tunnel carries an already-encrypted stream and cannot read it,
// which is not something a SOCKS proxy could be said to guarantee.

// TelegramHost is where the bot API lives.
const TelegramHost = "api.telegram.org:443"

// telegramBindAddr keeps the forward on loopback.
//
// A mapping written as a bare port number binds every interface, which would
// put an unauthenticated TCP path to api.telegram.org on this machine's public
// address. Nothing authenticates a forwarded connection — the token guards the
// tunnel's own channel, not the ports it exposes — so anyone who found the port
// would have a free relay to Telegram egressing from the peer's IP, and the
// port is only a random number in a 40000-wide range, which a port scan finds
// in seconds. The bot runs on this host and dials 127.0.0.1, so there is no
// reason for the listener to be reachable from anywhere else.
const telegramBindAddr = "127.0.0.1"

// telegramPortSuffix marks the mapping that carries the bot.
var telegramPortSuffix = "=" + TelegramHost

// isTelegramPort reports whether a mapping is the hidden Telegram forward.
func isTelegramPort(p string) bool {
	return strings.HasSuffix(strings.TrimSpace(p), telegramPortSuffix)
}

// telegramMappingPort reads the local port out of a Telegram mapping.
//
// The mapping binds loopback, so its left side is "127.0.0.1:41234" rather than
// a bare number. Older installs wrote the bare form, and those are still read
// here: refusing to recognise them would append a second mapping on every call
// and grow the port list without bound.
func telegramMappingPort(mapping string) (int, bool) {
	left := strings.TrimSpace(strings.SplitN(mapping, "=", 2)[0])
	if i := strings.LastIndex(left, ":"); i >= 0 {
		left = left[i+1:]
	}
	port, err := strconv.Atoi(left)
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}

// boundMapping returns the loopback form of a Telegram mapping that is
// currently bound to every interface, or "" when it is already on loopback.
func boundMapping(mapping string) string {
	left := strings.TrimSpace(strings.SplitN(mapping, "=", 2)[0])
	if strings.Contains(left, ":") {
		return "" // already carries a bind address
	}
	port, ok := telegramMappingPort(mapping)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s:%d%s", telegramBindAddr, port, telegramPortSuffix)
}

// EnsureTelegramPort makes sure a server tunnel forwards a local port straight
// to the Telegram API, and returns that port.
//
// It verifies the port is actually accepting connections rather than trusting
// the config. A mapping can be present and not live — written by an earlier
// attempt that never restarted the tunnel, or restored from a backup — and
// returning it then hands the bot a port that refuses every connection, which
// is exactly the failure this replaced.
func EnsureTelegramPort(name string) (int, error) {
	spec, err := loadServerSpec(name)
	if err != nil {
		return 0, err
	}

	for i, p := range spec.Ports {
		if !isTelegramPort(p) {
			continue
		}
		port, ok := telegramMappingPort(p)
		if !ok {
			continue
		}
		// An install from before the loopback bind carries the bare-port form,
		// which listens on every interface. Rewriting it here is the only thing
		// that closes that exposure on a machine already running: the mapping is
		// hidden from the port list, so nobody is going to correct it by hand.
		if bound := boundMapping(p); bound != "" {
			spec.Ports[i] = bound
			if _, err := spec.Save(); err != nil {
				return 0, err
			}
			RestartService(app.ServiceName(name))
			if waitPortAccepting(port, 15*time.Second) {
				return port, nil
			}
			return port, fmt.Errorf("moved the Telegram port (%d) on tunnel %q to loopback but it did not "+
				"start listening — check: journalctl -u %s -n 30", port, name, app.ServiceName(name))
		}
		if portAccepting(port) {
			return port, nil
		}
		// Configured but not live: the tunnel has not applied it.
		RestartService(app.ServiceName(name))
		if waitPortAccepting(port, 15*time.Second) {
			return port, nil
		}
		return port, fmt.Errorf("tunnel %q has a Telegram port (%d) configured but is not listening on it — "+
			"check: journalctl -u %s -n 30", name, port, app.ServiceName(name))
	}

	port := randomHighPort()
	spec.Ports = append(spec.Ports, fmt.Sprintf("%s:%d%s", telegramBindAddr, port, telegramPortSuffix))
	if _, err := spec.Save(); err != nil {
		return 0, err
	}
	RestartService(app.ServiceName(name))

	if !waitPortAccepting(port, 15*time.Second) {
		return port, fmt.Errorf("added the Telegram port (%d) to tunnel %q but it did not start listening — "+
			"check: journalctl -u %s -n 30", port, name, app.ServiceName(name))
	}
	return port, nil
}

// HasTelegramPort reports whether a tunnel already carries the Telegram forward.
func HasTelegramPort(name string) bool {
	spec, err := loadServerSpec(name)
	if err != nil {
		return false
	}
	for _, p := range spec.Ports {
		if isTelegramPort(p) {
			return true
		}
	}
	return false
}

// portAccepting reports whether something is listening on a loopback port.
func portAccepting(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// waitPortAccepting waits for a port to come up after a restart.
func waitPortAccepting(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if portAccepting(port) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
