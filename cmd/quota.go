package cmd

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/quota"
)

// The traffic limit, enforced where the bytes are counted.
//
// A limit checked from outside — by the panel, by a timer — is late by
// however long it waits between looks, and at a gigabit that is a gigabyte a
// second. Here the running total is read four times a second from the same
// counters the snapshot writes, and the generation is ended the moment it
// reaches the limit. The engine then does not start the tunnel again until the
// limit is raised or removed: the service stays up, nothing listens and
// nothing is dialled, which is what "offline" means for a tunnel.

// quotaCheck is how often the running total is compared with the limit, and
// quotaReread how often the limit file is read again while a tunnel runs, so
// a limit set or raised from the panel takes effect without a restart.
//
// Atomic because a test shortens them while the watcher of a generation that
// is winding down may still be reading them.
var quotaCheck, quotaReread atomic.Int64

func init() {
	quotaCheck.Store(int64(250 * time.Millisecond))
	quotaReread.Store(int64(2 * time.Second))
}

func every(v *atomic.Int64) time.Duration { return time.Duration(v.Load()) }

// live is the running generation's collector, for the limit to read. Set by
// startMetricsWithTraffic and cleared when that collector stops.
var liveCollector atomic.Pointer[metrics.Collector]

func quotaDir(configPath string) (dir, name string) {
	return filepath.Dir(configPath), tunnelNameFromPath(configPath)
}

// usedSoFar is what the tunnel has carried when nothing is running: its last
// written total.
func usedSoFar(configPath string) uint64 {
	dir, name := quotaDir(configPath)
	s, err := metrics.Read(dir, name)
	if err != nil {
		return 0
	}
	return s.BytesIn + s.BytesOut
}

// quotaReached reports whether the tunnel has used its limit up, from what is
// on disk.
func quotaReached(configPath string) bool {
	dir, name := quotaDir(configPath)
	q, err := quota.Load(dir, name)
	return err != nil || q.Reached(usedSoFar(configPath))
}

// awaitQuota holds a tunnel that has used its limit up until the limit is
// raised or removed. It reports false when the process is shutting down
// instead, and whether it had to wait at all.
func awaitQuota(ctx context.Context, configPath string) (ok, waited bool) {
	if !quotaReached(configPath) {
		return true, false
	}
	dir, name := quotaDir(configPath)
	q, err := quota.Load(dir, name)
	if err != nil {
		logger.Warnf("could not read traffic quota: %v — the tunnel stays offline until it can be read", err)
	} else {
		logger.Warnf("traffic limit reached: %s of %s used — the tunnel stays offline until its limit is raised",
			humanBytes(usedSoFar(configPath)), humanBytes(q.Limit))
	}
	t := time.NewTicker(every(&quotaReread))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, true
		case <-t.C:
		}
		if !quotaReached(configPath) {
			logger.Info("the traffic limit was raised; starting the tunnel again")
			return true, true
		}
	}
}

// watchQuota ends the generation when the running total reaches the limit.
func watchQuota(gen context.Context, configPath string, end func()) {
	dir, name := quotaDir(configPath)
	q, err := quota.Load(dir, name)
	if err != nil {
		logger.Warnf("could not read traffic quota: %v — taking the tunnel offline", err)
		end()
		return
	}
	check := time.NewTicker(every(&quotaCheck))
	defer check.Stop()
	reread := time.NewTicker(every(&quotaReread))
	defer reread.Stop()
	for {
		select {
		case <-gen.Done():
			return
		case <-reread.C:
			if next, err := quota.Load(dir, name); err == nil {
				q = next
			}
			continue
		case <-check.C:
		}
		if q.Limit == 0 {
			continue
		}
		c := liveCollector.Load()
		if c == nil {
			continue
		}
		if used := c.Total(); used >= q.Limit {
			logger.Warnf("traffic limit reached: %s of %s — taking the tunnel offline", humanBytes(used), humanBytes(q.Limit))
			end()
			return
		}
	}
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return itoa(int(n)) + " B"
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	whole := n / div
	tenth := (n % div) * 10 / div
	return itoa(int(whole)) + "." + itoa(int(tenth)) + " " + string("KMGTP"[exp]) + "B"
}
