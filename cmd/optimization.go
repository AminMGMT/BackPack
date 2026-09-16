package cmd

import (
	"os/exec"
	"runtime"
	"syscall"

	"github.com/backpack/backpack/internal/optimize"
)

// ApplyTCPTuning applies the socket and TCP settings a tunnel wants, at start.
//
// It used to carry its own table of values, and that table disagreed with the
// one Optimize writes. The disagreements were not visible from anywhere:
// Optimize set net.core.rmem_default to 16 MB and the next tunnel start put it
// back to 1 MB, the same for wmem_default, and tcp_notsent_lowat went from
// 128 KB to 32 KB the same way. An operator who had deliberately optimized the
// machine had three of those settings quietly undone by a restart.
//
// ip_local_port_range was the fourth and the one that was visible, because
// losing a service's port is a thing people notice. It is the reason this now
// takes its values from optimize.EngineStartupTuning rather than keeping a
// second copy of them — one table means the two cannot drift, and which keys a
// starting tunnel may set is decided there, beside the values.
func ApplyTCPTuning() {
	if runtime.GOOS != "linux" {
		logger.Info("Non-Linux system detected, skipping TCP optimizations.")
		return
	}
	logger.Info("Applying TCP optimizations for Linux...")

	for _, kv := range optimize.EngineStartupTuning() {
		if err := exec.Command("sysctl", "-w", kv[0]+"="+kv[1]).Run(); err != nil {
			// Warn, not error: a container without CAP_SYS_ADMIN refuses these
			// and the tunnel runs perfectly well without them. A log full of
			// harmless red is how people learn to stop reading it.
			logger.Warnf("could not set %s=%s (%v) — continuing", kv[0], kv[1], err)
		} else {
			logger.Debugf("set %s=%s", kv[0], kv[1])
		}
	}

	// Set file descriptor limit programmatically
	{
		var rLimit syscall.Rlimit
		err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
		if err != nil {
			logger.Errorf("Error getting Rlimit: %v", err)
		} else {
			logger.Debugf("Current file descriptor limit: %d", rLimit.Cur)

			// Set the maximum and current file descriptor limits to 1048576
			rLimit.Max = 1048576
			rLimit.Cur = 1048576
			err = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit)
			if err != nil {
				// Expected wherever the process is not privileged — an
				// unprivileged container, a CI runner, a hardened host. The
				// tunnel runs perfectly well on the limit it already has, so
				// this is a warning about a tuning step that did not happen,
				// not an error about something that went wrong. Logging it at
				// ERROR is how a log full of harmless red teaches people to
				// stop reading it.
				logger.Warnf("could not raise the file descriptor limit (%v) — continuing on the current limit of %d", err, rLimit.Cur)
			} else {
				logger.Debugf("Successfully set file descriptor limit to: %d", rLimit.Cur)
			}
		}
	}
}
