package manage

import "time"

// Reading a service's journal is expensive, and it was done once per request.
//
// The panel's log drawer polls /api/logs every two seconds while it is open,
// and every one of those forked `journalctl -n 150`, which makes journald seek,
// decompress and filter its way through the unit's entries. One drawer is a
// steady load; two panels open on two screens is twice it; and on a host with a
// large journal a single read can take longer than the two seconds before the
// next one is due — at which point the requests overlap, each overlap adds
// another journalctl, and journald and anything reading from it (rsyslog, where
// imjournal is configured) climb until somebody closes the tab.
//
// Nothing about the answer changes fast enough to be worth that. A tunnel's
// last hundred and fifty lines are the same lines a moment later. See ttlcache
// for the sharing this relies on.

// logsCacheTTL is how long one journal read is served for. It matches the
// panel's own poll interval: a drawer polling on its timer then causes one read
// per tick however many drawers there are, which is the floor this can have
// without the display going stale.
const logsCacheTTL = 2 * time.Second

// logsCachePrune is how long an untouched entry is kept before it is dropped.
const logsCachePrune = 5 * time.Minute

var journalCache = newTTLCache[string](logsCacheTTL, logsCachePrune)
