package cmd

import (
	"fmt"
	"os/exec"
	"runtime"
	"syscall"
)

// applyTCPTuning applies temporary TCP optimizations for Linux to handle massive connections
func ApplyTCPTuning() {
	if runtime.GOOS == "linux" {
		logger.Info("Applying TCP optimizations for Linux...")

		// Define the buffer sizes to try
		bufferSizes := []int{
			256 * 1024 * 1024, // 256MB
			128 * 1024 * 1024, // 128MB
			64 * 1024 * 1024,  // 64MB
			32 * 1024 * 1024,  // 32MB
			16 * 1024 * 1024,  // 16MB
		}

		// Loop through buffer sizes and attempt to apply them
		for _, size := range bufferSizes {
			// Build the command with the current buffer size
			cmd := []string{"sysctl", "-w", fmt.Sprintf("net.core.rmem_max=%d", size)}
			if err := exec.Command(cmd[0], cmd[1:]...).Run(); err == nil {
				logger.Printf("Successfully set rmem_max to %d\n", size)
				break // Exit the loop if successful
			} else {
				logger.Debugf("Failed to set rmem_max to %d, trying next lower value...\n", size)
			}
		}

		// Same for wmem_max
		for _, size := range bufferSizes {
			// Build the command with the current buffer size for wmem_max
			cmd := []string{"sysctl", "-w", fmt.Sprintf("net.core.wmem_max=%d", size)}
			if err := exec.Command(cmd[0], cmd[1:]...).Run(); err == nil {
				logger.Printf("Successfully set wmem_max to %d\n", size)
				break // Exit the loop if successful
			} else {
				logger.Debugf("Failed to set wmem_max to %d, trying next lower value...\n", size)
			}
		}

		// Commands for optimizing TCP parameters.
		//
		// The ephemeral port range is deliberately not here.
		//
		// It used to be, as "1024 65535", and that made a closed loop out of the
		// one setting this program had already decided was wrong: Optimize
		// writes 32768-60999 to /etc/sysctl.d/99-backpack.conf, the health check
		// tells the operator to run it, and then the next engine start — every
		// engine start, on every tunnel — widened it straight back. A server
		// could be optimized, pass its own check, and be wide again the moment a
		// tunnel restarted, with nothing anywhere saying so.
		//
		// The range belongs to Optimize, which is the one place that reasons
		// about it and the one place that persists it. An engine starting a
		// tunnel has no business rewriting a machine-wide kernel setting that
		// decides whether unrelated services can keep their own ports. See the
		// note over ip_local_port_range in internal/optimize/optimize.go for
		// what widening it actually costs.
		commands := [][]string{
			{"sysctl", "-w", "net.ipv4.tcp_tw_reuse=1"},            // Reuse TIME_WAIT sockets
			{"sysctl", "-w", "net.ipv4.tcp_fin_timeout=15"},        // Reduce TCP FIN timeout
			{"sysctl", "-w", "net.core.somaxconn=65536"},           // Increase max queue length of incoming connections
			{"sysctl", "-w", "net.ipv4.tcp_max_syn_backlog=20480"}, // Increase SYN request backlog
			{"sysctl", "-w", "net.ipv4.tcp_window_scaling=1"},      // Enable TCP window scaling
			{"sysctl", "-w", "net.ipv4.tcp_fastopen=3"},            // Enable TCP Fast Open
			// {"sysctl", "-w", "net.ipv4.tcp_rmem = 16384 1048576 33554432"}, // Maximum of 1MB of TCP read buffer memory
			// {"sysctl", "-w", "net.ipv4.tcp_wmem = 16384 1048576 33554432"}, // Maximum of 1MB TCP write buffer memory
			{"sysctl", "-w", "net.ipv4.tcp_notsent_lowat=32768"}, // Do not allow more than 4096 bytes of unsent data in buffer
			//{"sysctl", "-w", "net.core.rmem_max=26214400"},       // Set maximum TCP receive buffer size
			//{"sysctl", "-w", "net.core.wmem_max=26214400"},       // Set maximum TCP send buffer size
			{"sysctl", "-w", "net.core.rmem_default=1048576"}, // Set default TCP receive buffer size
			{"sysctl", "-w", "net.core.wmem_default=1048576"}, // Set default TCP send buffer size
		}

		// Execute the sysctl commands
		for _, cmd := range commands {
			err := exec.Command(cmd[0], cmd[1:]...).Run()
			if err != nil {
				logger.Errorf("Failed to apply TCP tuning: %s", cmd)
			} else {
				logger.Debugf("Successfully applied: %s", cmd)
			}
		}

		// Set file descriptor limit programmatically
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
	} else {
		logger.Info("Non-Linux system detected, skipping TCP optimizations.")
	}
}
