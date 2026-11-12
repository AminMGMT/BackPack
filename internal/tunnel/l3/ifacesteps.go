package l3

import (
	"fmt"
	"strconv"
	"strings"
)

// interfaceSteps is the ip(8) commands, without the "ip", that give a new
// interface its address and MTU and bring it up. Both kinds of link the engine
// makes go through it — the TUN device and the kernel GRE link — so the two are
// configured identically, and it is pure so what they are asked to do can be
// tested without a kernel.
//
// Two address forms are supported, because the two describe different things.
// A prefix shorter than /32 — "10.10.0.1/30" — installs a subnet route, and
// the peer is reached because it falls inside that subnet. A bare address with
// an explicit peer installs a point-to-point route to exactly one host, which
// is the tighter description of what this tunnel actually is.
func interfaceSteps(name, localIP, peerIP string, mtu, txQueueLen int) ([][]string, error) {
	addrArgs := []string{"addr", "add", localIP, "dev", name}
	if !strings.Contains(localIP, "/") {
		if peerIP == "" {
			return nil, fmt.Errorf("l3: local_ip %q has no prefix length, so peer_ip is required", localIP)
		}
		addrArgs = []string{"addr", "add", localIP, "peer", peerIP, "dev", name}
	}

	steps := [][]string{
		addrArgs,
		{"link", "set", "dev", name, "mtu", strconv.Itoa(mtu)},
		// The queue between the kernel and whatever drains the interface.
		//
		// A TUN device's default is 500 packets, which is sized for a device
		// drained by hardware. Here it is drained by one goroutine that reads
		// a packet, encapsulates it, encrypts it and writes it to a socket —
		// several microseconds each, during which the kernel keeps queueing.
		// Under a bulk transfer the queue fills and the kernel drops the
		// overflow, which showed up in the field as ~2% of the acknowledgements
		// on a 100 MB download being lost. TCP recovers, so the transfer still
		// completes and verifies, but every one of those drops costs a
		// retransmit that the link did not need.
		//
		// A deeper queue is the cheap half of the fix and the one with no
		// downside worth the name: these are small packets, so even a full
		// queue is a fraction of a megabyte, and latency is bounded by the
		// drain rate rather than by the depth.
		{"link", "set", "dev", name, "txqueuelen", strconv.Itoa(txQueueLen)},
		{"link", "set", "dev", name, "up"},
	}

	// A /30 or wider gives the peer a subnet route already. A prefix that
	// covers only this host does not, so the route has to be stated.
	if peerIP != "" && needsExplicitPeerRoute(localIP) {
		steps = append(steps, []string{"route", "replace", peerIP, "dev", name})
	}
	return steps, nil
}

// needsExplicitPeerRoute reports whether the local address covers only itself,
// leaving the peer unreachable without a route of its own.
func needsExplicitPeerRoute(localIP string) bool {
	_, prefix, found := strings.Cut(localIP, "/")
	if !found {
		// The point-to-point form already installed the peer route.
		return false
	}
	return prefix == "32" || prefix == "128"
}
