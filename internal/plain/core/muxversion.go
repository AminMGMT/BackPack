package core

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/xtaci/smux"
)

// The plain wire does not negotiate the mux version: each end uses the
// mux_version in its own file, and smux drops a session whose frames carry
// another. The tunnel then reported itself up and carried nothing, and the
// only trace was a session closing at debug level. A file edited by hand on
// one server is enough to get there.

// muxMismatchSaid is when the mismatch was last logged, so a pool reopening
// its sessions says it once a minute rather than once a session.
var muxMismatchSaid atomic.Int64

// NoteMuxMismatch logs, at most once a minute, that a mux session ended
// because the other end speaks another mux version. It reports whether err
// was that.
func NoteMuxMismatch(logger *logrus.Logger, err error, version int) bool {
	if !errors.Is(err, smux.ErrInvalidProtocol) {
		return false
	}
	now := time.Now().Unix()
	if last := muxMismatchSaid.Load(); now-last >= 60 && muxMismatchSaid.CompareAndSwap(last, now) {
		logger.Errorf("the other server uses a different mux_version (this one uses %d), so no traffic can pass: "+
			"set the same mux_version on both servers and restart the tunnel", version)
	}
	return true
}
