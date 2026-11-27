package core

import (
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
)

// A backend list: one forwarded port served by several backends on the
// kharej, written "8443|127.0.0.1:8444" in the port mapping. Backpack.
//
// The engine this was ported from had no such thing, and on its transports a
// list was handed to the dialer whole and failed on every connection. Here
// each new connection starts at the next backend in turn and moves on to the
// one after when a backend refuses, so the load is spread and a backend that is
// down costs a connection a failed dial rather than the connection.

// backendTurns counts, per list, how many connections have started on it.
var backendTurns sync.Map // string → *atomic.Uint64

// backendOrder is the list's backends in the order the next connection tries
// them: from its turn onwards, wrapping round.
func backendOrder(list string) []string {
	parts := strings.Split(list, "|")
	if len(parts) == 1 {
		return parts
	}
	v, _ := backendTurns.LoadOrStore(list, new(atomic.Uint64))
	start := int((v.(*atomic.Uint64).Add(1) - 1) % uint64(len(parts)))
	return append(append([]string{}, parts[start:]...), parts[:start]...)
}

// DialBackend dials the backends of list in turn until one answers. A single
// address is dialled exactly as before.
func DialBackend(list string, dial func(addr string) (*net.TCPConn, error)) (*net.TCPConn, error) {
	var errs []error
	for _, addr := range backendOrder(list) {
		c, err := dial(addr)
		if err == nil {
			return c, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// PickBackend is the backend a new UDP flow goes to: the next in turn. A UDP
// backend cannot be seen refusing, so there is no moving on.
func PickBackend(list string) string {
	return backendOrder(list)[0]
}
