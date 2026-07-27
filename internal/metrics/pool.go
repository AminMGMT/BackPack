package metrics

import "sync/atomic"

// What the connection pool is actually doing.
//
// The pool is allowed to outgrow the size that was configured for it: it adds
// connections when the server asks for them faster than it supplies them, and —
// since the throughput trigger — when the connections it already has are each
// carrying enough to suggest another would help. Both are bounded, and both are
// invisible.
//
// That invisibility is a problem of its own. Somebody who set a pool of 8 and
// sees thirty-odd connections to their server has no way to tell a working
// tunnel under load from a leak, and the honest answer — "it grew on purpose,
// here is the throughput that grew it" — is already computed once a second and
// then thrown away.
//
// One tunnel runs per process, so a single set of values needs no key. Nothing
// is reported by the transports that do not scale a pool (the server side, the
// datagram listeners), and a zero configured size is what "this tunnel has no
// pool" looks like — the panel then shows nothing rather than "0 of 0".

var (
	poolLive       atomic.Int64 // connections open right now
	poolTarget     atomic.Int64 // size the pool is currently aiming for
	poolConfigured atomic.Int64 // size the operator asked for
	poolMbps       atomic.Int64 // throughput over the last interval
)

// ReportPool records the state of the connection pool. Called by the client
// transports that maintain one, once per pool tick.
func ReportPool(live, target, configured, mbps int) {
	poolLive.Store(int64(live))
	poolTarget.Store(int64(target))
	poolConfigured.Store(int64(configured))
	poolMbps.Store(int64(mbps))
}

// ClearPool forgets the pool state on disconnect or restart, so a stale size is
// not reported as current.
func ClearPool() {
	poolLive.Store(0)
	poolTarget.Store(0)
	poolConfigured.Store(0)
	poolMbps.Store(0)
}

// PoolState returns the reported pool state.
func PoolState() (live, target, configured, mbps int) {
	return int(poolLive.Load()), int(poolTarget.Load()),
		int(poolConfigured.Load()), int(poolMbps.Load())
}
