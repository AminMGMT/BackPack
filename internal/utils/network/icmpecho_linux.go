//go:build linux

package network

import (
	"os"
	"strings"
	"sync"
)

// The ICMP spoof profile carries the tunnel's data inside Echo Requests. A raw
// socket lifts those out, but the host kernel ALSO sees each one and answers it
// with an automatic Echo Reply — sent to the packet's source, which is forged.
// The reply is therefore misdirected and harmless to the tunnel, but on the
// download path it is one extra outbound packet for every data packet received,
// which at real bandwidth is a needless tax on the uplink and a clear marker of
// the flow. The reference spoof-tunnel silences it with
//
//	sysctl -w net.ipv4.icmp_echo_ignore_all=1
//
// and restores it on shutdown. This does the same, but refcounted: several ICMP
// spoof carriers may be open at once (a multi-tunnel host), and the setting must
// stay in force until the last of them closes, then return to whatever it was
// before the first opened — never restored out from under a carrier still using
// it.

const icmpEchoIgnoreProc = "/proc/sys/net/ipv4/icmp_echo_ignore_all"

var icmpEcho struct {
	mu   sync.Mutex
	refs int    // how many ICMP-receive carriers currently want replies silenced
	prev string // the value in place before the first of them acquired
}

// acquireICMPEchoSuppression silences the kernel's automatic Echo Replies for as
// long as at least one caller holds it. The first caller records the previous
// setting and switches it to "1"; later callers only bump the count. Best
// effort: if the proc file cannot be written (not root, a hardened kernel), the
// tunnel still works — the kernel just keeps emitting the misdirected replies,
// exactly as before this guard existed — so a failure is swallowed rather than
// killing the carrier.
func acquireICMPEchoSuppression() {
	icmpEcho.mu.Lock()
	defer icmpEcho.mu.Unlock()
	icmpEcho.refs++
	if icmpEcho.refs > 1 {
		return
	}
	if b, err := os.ReadFile(icmpEchoIgnoreProc); err == nil {
		icmpEcho.prev = strings.TrimSpace(string(b))
	} else {
		icmpEcho.prev = "0"
	}
	if icmpEcho.prev != "1" {
		_ = os.WriteFile(icmpEchoIgnoreProc, []byte("1\n"), 0o644)
	}
}

// releaseICMPEchoSuppression drops one hold; the last one restores the setting
// the first caller found. Safe to call once per successful acquire.
func releaseICMPEchoSuppression() {
	icmpEcho.mu.Lock()
	defer icmpEcho.mu.Unlock()
	if icmpEcho.refs == 0 {
		return
	}
	icmpEcho.refs--
	if icmpEcho.refs > 0 {
		return
	}
	if icmpEcho.prev != "1" {
		_ = os.WriteFile(icmpEchoIgnoreProc, []byte(icmpEcho.prev+"\n"), 0o644)
	}
}
