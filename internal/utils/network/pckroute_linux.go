//go:build linux

package network

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Working out, without being told, how a packet built here should leave the
// machine.
//
// paqet — the tool this transport takes its approach from — asks the operator
// for the interface, the local address and the gateway's MAC, and its README
// spends a screen explaining how to find each on three operating systems. That
// is a fair design for a research tool and a bad one for a tunnel somebody
// installs on a VPS at two in the morning: three values, each of which silently
// produces a tunnel that connects and carries nothing when it is wrong.
//
// All three are already knowable. The route to the peer names the interface and
// the next hop; the interface knows its own addresses and MAC; the neighbour
// table knows the next hop's. So they are looked up, and the config fields exist
// only to override a wrong guess — which is the right way round.

// pckEgress is everything the carrier needs to put a frame on the wire toward
// one peer.
type pckEgress struct {
	Iface    *net.Interface
	LocalIP  net.IP
	SrcMAC   net.HardwareAddr
	NextHop  net.HardwareAddr // nil when the link has no L2 addressing
	LinkLen  int              // 14 on Ethernet, 0 on a point-to-point device
	Ethernet bool
}

// discoverEgress resolves the route to dst into the frame's addressing.
//
// ifaceName and gatewayMAC override the lookup when they are set. A failure to
// find the next hop's MAC is not an error: the carrier falls back to sending
// through a raw IP socket, where the kernel does the L2 work. Only a failure to
// identify the interface or the local address is fatal, because nothing can be
// built without those.
func discoverEgress(dst net.IP, ifaceName, gatewayMAC string) (*pckEgress, error) {
	dst = dst.To4()
	if dst == nil {
		return nil, fmt.Errorf("pck: only IPv4 peers are supported")
	}

	name := ifaceName
	var gwIP net.IP
	if name == "" {
		var err error
		name, gwIP, err = routeToward(dst)
		if err != nil {
			return nil, err
		}
	} else if _, gw, err := routeToward(dst); err == nil {
		gwIP = gw
	}

	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, fmt.Errorf("pck: no such interface %q: %w", name, err)
	}

	local := localSourceToward(dst)
	if local == nil || local.Equal(net.IPv4zero) {
		// The route said which device; ask the device for an address on it.
		local = firstIPv4Of(iface)
	}
	if local == nil {
		return nil, fmt.Errorf("pck: %s has no IPv4 address to send from", iface.Name)
	}

	e := &pckEgress{
		Iface:    iface,
		LocalIP:  local.To4(),
		SrcMAC:   iface.HardwareAddr,
		Ethernet: len(iface.HardwareAddr) == 6,
	}
	if e.Ethernet {
		e.LinkLen = pckEthHeaderLen
	}

	// Only an Ethernet link has a next hop to address. On a tun, a PPP link or
	// anything else without L2, the frame is the IP packet and there is nothing
	// to look up.
	if !e.Ethernet {
		return e, nil
	}

	if gatewayMAC != "" {
		mac, err := parseMAC(gatewayMAC)
		if err != nil {
			return nil, err
		}
		e.NextHop = mac
		return e, nil
	}

	// On a link where the peer is directly attached there is no gateway; the
	// next hop is the peer itself.
	target := gwIP
	if target == nil {
		target = dst
	}
	e.NextHop = neighbourMAC(iface.Name, target)
	return e, nil
}

// firstUsableIface names an interface a pck listener could answer from: up, not
// loopback, and holding an ordinary IPv4 address.
//
// It is the fallback for a server whose route lookup found nothing — see
// newPckConn. Deliberately the first rather than the best: with no route table
// to consult there is nothing to rank them by, and an operator who needs a
// particular one sets pck_interface.
func firstUsableIface() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, in := range ifaces {
		if in.Flags&net.FlagUp == 0 || in.Flags&net.FlagLoopback != 0 {
			continue
		}
		if firstIPv4Of(&in) != nil {
			return in.Name
		}
	}
	return ""
}

// firstIPv4Of returns an interface's first ordinary IPv4 address.
func firstIPv4Of(iface *net.Interface) net.IP {
	addrs, err := iface.Addrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() && !ip4.IsLinkLocalUnicast() {
			return ip4
		}
	}
	return nil
}

// routeToward reads /proc/net/route and returns the interface a packet to dst
// would leave by, and the gateway it would go via (nil when dst is on-link).
//
// /proc is parsed rather than netlink queried on purpose: it is a dozen lines
// of text with no dependency, no privilege and no cgo, and this is a lookup
// done once per tunnel at startup rather than per packet.
func routeToward(dst net.IP) (iface string, gateway net.IP, err error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return "", nil, fmt.Errorf("pck: cannot read the routing table: %w", err)
	}
	defer f.Close()

	want := binary.BigEndian.Uint32(dst.To4())

	bestLen := -1
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 {
			continue
		}
		// The address columns are little-endian hex, as the kernel stores them.
		net32, ok1 := hexLE(f[1])
		gw32, ok2 := hexLE(f[2])
		mask32, ok3 := hexLE(f[7])
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		if want&mask32 != net32 {
			continue
		}
		// Longest prefix wins, which is what the kernel would pick too.
		if l := maskLen(mask32); l > bestLen {
			bestLen = l
			iface = f[0]
			if gw32 == 0 {
				gateway = nil
			} else {
				g := make(net.IP, 4)
				binary.BigEndian.PutUint32(g, gw32)
				gateway = g
			}
		}
	}
	if iface == "" {
		return "", nil, fmt.Errorf("pck: no route to %s", dst)
	}
	return iface, gateway, nil
}

// hexLE parses one of /proc/net/route's little-endian hex address columns into
// a host-order uint32.
func hexLE(s string) (uint32, bool) {
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, false
	}
	// The column is the address's bytes in little-endian order; flip them so
	// the result reads as a normal IPv4 address.
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(v))
	return binary.BigEndian.Uint32(b[:]), true
}

// maskLen counts the leading ones of a netmask, so routes can be ranked by
// how specific they are.
func maskLen(mask uint32) int {
	n := 0
	for i := 31; i >= 0; i-- {
		if mask&(1<<uint(i)) == 0 {
			break
		}
		n++
	}
	return n
}

// neighbourMAC looks up an address in the kernel's neighbour table, priming it
// first if the entry is not there.
//
// The priming matters more than it looks. On a machine that has just booted, or
// one where the entry has aged out, the table simply has no line for the
// gateway — and a carrier that treated that as "this link has no next hop"
// would silently fall back to the slower path forever. Sending one datagram
// toward the peer makes the kernel resolve the address, after which the entry
// is there to read.
func neighbourMAC(iface string, ip net.IP) net.HardwareAddr {
	if mac := readARP(iface, ip); mac != nil {
		return mac
	}
	// One throwaway packet to a discard port, purely to make the kernel ARP.
	if c, err := net.DialTimeout("udp4", net.JoinHostPort(ip.String(), "9"), time.Second); err == nil {
		_, _ = c.Write([]byte{0})
		c.Close()
	}
	for i := 0; i < 20; i++ {
		if mac := readARP(iface, ip); mac != nil {
			return mac
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

// readARP scans /proc/net/arp for a resolved entry on the given device.
func readARP(iface string, ip net.IP) net.HardwareAddr {
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return nil
	}
	defer f.Close()

	want := ip.String()
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 6 || f[0] != want || f[5] != iface {
			continue
		}
		// Flag 0x2 is ATF_COM: the entry is complete. An incomplete one holds
		// the all-zero address, which would send every frame into a black hole.
		if flags, err := strconv.ParseUint(strings.TrimPrefix(f[2], "0x"), 16, 32); err != nil || flags&0x2 == 0 {
			continue
		}
		mac, err := net.ParseMAC(f[3])
		if err != nil || len(mac) != 6 {
			continue
		}
		return mac
	}
	return nil
}
