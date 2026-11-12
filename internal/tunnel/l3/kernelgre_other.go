//go:build !linux

package l3

import (
	"context"
	"fmt"
	"runtime"
)

// Kernel GRE is a Linux link type, so everywhere else the engine refuses it
// plainly, the way it refuses a TUN device.

func (t *Tunnel) runKernelGRE(ctx context.Context) error {
	return fmt.Errorf("l3: carrier %q is a Linux kernel GRE link, which %s does not provide", t.cfg.Carrier, runtime.GOOS)
}

func readGRECounters(string) (greCounters, bool) { return greCounters{}, false }

// CleanupGRE has nothing to clean where there is no kernel GRE.
func CleanupGRE(iface, carrier, addr string) {}
