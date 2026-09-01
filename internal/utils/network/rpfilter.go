package network

import (
	"net"
	"os"
	"strings"
)

// Reverse-path filtering, and why a forged-source tunnel has to care about it.
//
// The spoof carrier receives on ordinary AF_INET sockets, which sit above the
// kernel's IP input. A strict rp_filter (=1) drops a packet whose source is not
// reachable back out the interface it arrived on — which every forged source is
// — before the tunnel ever sees it. The tunnel comes up, reports itself
// connected, and carries nothing. It is the most common reason a spoof tunnel
// is silently dead, so both the startup check and Health Check test for it, and
// share this so they agree on what they measure.
//
// The value that decides it is not conf.all alone: the kernel applies the
// MAXIMUM of conf.all and the receiving interface's own setting. A host with
// all=0 and eth0=1 filters exactly as if it were strict everywhere — which a
// check reading only conf.all passes, leaving a silent tunnel with a clean bill
// of health.

// readRPFilterKey reports one conf/<key>/rp_filter value, or -1 if it cannot be
// read (non-Linux, or the file is absent). 0 = off, 1 = strict, 2 = loose.
func readRPFilterKey(key string) int {
	b, err := os.ReadFile("/proc/sys/net/ipv4/conf/" + key + "/rp_filter")
	if err != nil {
		return -1
	}
	switch strings.TrimSpace(string(b)) {
	case "0":
		return 0
	case "1":
		return 1
	case "2":
		return 2
	}
	return -1
}

// EffectiveRPFilter is the rp_filter the kernel actually applies to packets
// arriving on iface: the MAXIMUM of conf.all and conf.<iface>. It returns that
// value and the sysctl key it came from, so a caller can name the setting to
// change. 1 (strict) is the only value that drops forged sources; 0 and 2 pass
// them.
//
// An empty or unreadable interface falls back to conf.all alone — still better
// than never looking past it, it just cannot catch an interface stricter than
// all.
func EffectiveRPFilter(iface string) (value int, key string) {
	value, key = readRPFilterKey("all"), "net.ipv4.conf.all.rp_filter"
	if iface == "" {
		return value, key
	}
	if v := readRPFilterKey(iface); v > value {
		value, key = v, "net.ipv4.conf."+iface+".rp_filter"
	}
	return value, key
}

// InterfaceTowardPeer names the interface the kernel would receive the peer's
// packets on — the one it routes toward the peer's real address. Empty when it
// cannot be determined, which is not an error: the caller falls back to
// conf.all.
func InterfaceTowardPeer(peerReal string) string {
	ip := net.ParseIP(strings.TrimSpace(peerReal))
	if ip == nil {
		return ""
	}
	// The local address the kernel picks to reach the peer identifies the
	// interface — the same trick the carrier uses to resolve its own source.
	conn, err := net.Dial("udp", net.JoinHostPort(ip.String(), "9"))
	if err != nil {
		return ""
	}
	defer conn.Close()
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.Equal(local.IP) {
				return ifc.Name
			}
		}
	}
	return ""
}
