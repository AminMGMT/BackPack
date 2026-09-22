package network

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Dialling several addresses at once and keeping the first that answers.
//
// # The problem
//
// The endpoint list is walked one at a time: dial the current address, and on
// failure rotate to the next. That is correct and it is slow in the one case it
// exists for. A filtered address does not *refuse* a connection — it swallows
// it — so a dead first address costs a full dial timeout, ten seconds on the
// shipped default, on every reconnect. With three addresses and the working one
// last, a tunnel is down for twenty seconds before it starts trying the thing
// that works, every time it reconnects.
//
// This is the shape browsers call happy eyeballs, for the same reason: the
// alternative to racing is waiting out a timeout on something that will never
// answer.
//
// # What it does not cover, and this is worth knowing
//
// This races the *dial*. It helps against an address that drops the SYN and
// against one that refuses — between them, most of what a filtered IP does.
//
// It does not help against the third shape: an address that completes the TCP
// handshake and then swallows everything after it, which is what a DPI box
// doing protocol inspection looks like. That connection wins the race, because
// by the only measure a dial has it worked, and the tunnel then stalls in the
// control-channel handshake instead of in connect().
//
// Covering that means racing through the handshake rather than to the socket,
// which is a different and larger change: the handshake is transport-specific
// where the dial is not. The transport fallback chain (internal/tunnel/chain)
// is the mechanism that does eventually get past it, on a longer timescale.
//
// # The stagger
//
// Racing every address from the same instant would open N connections on every
// reconnect and throw away N-1 of them. That is rude to the servers, wasteful
// on a metered link, and unnecessary: the first address is first because it is
// preferred. So each subsequent dial waits out a stagger, and an address that
// answers inside its own head start is never raced at all.
//
// # The part that makes it safe
//
// Exactly one connection survives, and every other one is closed. A racer that
// leaks the runners-up leaks a socket per reconnect — on the code path that
// runs most often precisely when the network is at its worst.

// Race dials addrs concurrently, staggered, and returns the first that answers.
//
// dial is called once per address with a context that is cancelled as soon as
// the race is decided. Whatever it returns is closed for every address but the
// winner, if it is something that can be closed.
//
// The zero value of T is returned with an error when nothing answers, and the
// error names every address and what it said — "connection refused" and "i/o
// timeout" send an operator to completely different places.
func Race[T any](parent context.Context, addrs []string, stagger time.Duration,
	dial func(context.Context, string) (T, error)) (T, string, error) {

	var zero T
	if len(addrs) == 0 {
		return zero, "", errors.New("no address to dial")
	}
	if len(addrs) == 1 {
		// The ordinary case, and it must cost nothing extra: no goroutine, no
		// timer, no context wrapping.
		conn, err := dial(parent, addrs[0])
		return conn, addrs[0], err
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	type result struct {
		conn T
		addr string
		err  error
	}
	results := make(chan result, len(addrs))

	var wg sync.WaitGroup
	for i, addr := range addrs {
		wg.Add(1)
		go func(i int, addr string) {
			defer wg.Done()
			// The head start. A working first address is never raced.
			if i > 0 {
				t := time.NewTimer(time.Duration(i) * stagger)
				defer t.Stop()
				select {
				case <-ctx.Done():
					results <- result{addr: addr, err: ctx.Err()}
					return
				case <-t.C:
				}
			}
			conn, err := dial(ctx, addr)
			results <- result{conn: conn, addr: addr, err: err}
		}(i, addr)
	}

	// Close whatever loses, once every dial has finished. Cancelling the
	// context above is what makes them finish; this is what makes sure nothing
	// they produced is left open.
	go func() {
		wg.Wait()
		close(results)
	}()

	var (
		won      bool
		winner   T
		wonAddr  string
		failures []string
	)
	for r := range results {
		// A dialler that returns no connection and no error has not won
		// anything. Treating it as a winner would hand the caller nothing to
		// use and close the connection that actually worked.
		if r.err == nil && !isNil(r.conn) {
			if !won {
				won, winner, wonAddr = true, r.conn, r.addr
				cancel() // stop the others; they are drained below
				continue
			}
			closeIfPossible(r.conn)
			continue
		}
		if r.err != nil && !won {
			failures = append(failures, fmt.Sprintf("%s: %v", r.addr, r.err))
		}
	}

	if won {
		return winner, wonAddr, nil
	}
	return zero, "", fmt.Errorf("no address answered — %s", strings.Join(failures, "; "))
}

// closeIfPossible closes a losing dial's result. The racer is generic over what
// a dial produces — a net.Conn, a websocket, a KCP session — and the one thing
// they all have in common is that leaving them open is a leak.
func closeIfPossible(v any) {
	if c, ok := v.(io.Closer); ok && c != nil {
		_ = c.Close()
	}
}

// isNil reports whether a dial produced nothing at all.
//
// A plain `any(v) == nil` is not enough: a typed nil pointer in an interface is
// not equal to nil, and a dialler that returns (*net.TCPConn)(nil) with no
// error would otherwise win the race and hand the caller something that panics
// on first use.
func isNil(v any) bool {
	if v == nil {
		return true
	}
	if c, ok := v.(interface{ IsNil() bool }); ok {
		return c.IsNil()
	}
	return isNilValue(v)
}
