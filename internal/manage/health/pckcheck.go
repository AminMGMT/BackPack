package health

import (
	"fmt"
	"net"
	"strings"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tunnel/paqet"
	"github.com/backpack/backpack/internal/utils/network"
)

// What a PCK tunnel needs from the machine.
//
// It has no network interface to look for: it is paqet's engine, which captures
// and injects TCP segments with libpcap. Three things stand between it and a
// working tunnel, and each of them fails without any message from the tunnel
// itself — which is the reason they are checked here rather than left to the
// log:
//
//   - libpcap. A build without it cannot open the capture at all (ErrNoPcap).
//   - the firewall guard. The kernel answers the tunnel's segments, addressed to
//     a port nothing listens on, with RSTs, and the first stateful device on
//     the path reads that as the flow ending. Three iptables rules drop them.
//     Without iptables, or with the rules gone, the tunnel connects and then
//     dies a few seconds in.
//   - the gateway. A frame is injected with an Ethernet header, so the next
//     hop's hardware address must be known: the dialling side finds it in the
//     neighbour table, and when the gateway has not answered ARP there is no
//     frame to send.

// pckHost is what the checks ask the machine, as functions so a test can answer
// for a machine that is not there.
type pckHost struct {
	pcap    bool
	path    func(peer net.IP, iface, gatewayMAC string) (*network.PckPath, error)
	guard   func(token string) network.PckGuardStatus
	resolve func(host string) ([]net.IP, error)
}

// livePckHost asks this machine.
var livePckHost = pckHost{
	pcap:    paqet.PcapAvailable,
	path:    network.DiscoverPckPath,
	guard:   network.PckGuardState,
	resolve: net.LookupIP,
}

// pckChecks is the diagnosis for a PCK tunnel's file, in place of the interface
// check the TUN carriers get.
func pckChecks(g string, l config.L3Config) []Check { return pckChecksOn(g, l, livePckHost) }

func pckChecksOn(g string, l config.L3Config, h pckHost) []Check {
	var out []Check
	if !h.pcap {
		// Nothing below can work, and its failures would only repeat this one.
		return append(out, Check{Group: g, Name: "Packet capture", Level: CheckFail,
			Detail: paqet.ErrNoPcap.Error(),
			Fix:    "install the official release binary (sudo backpack → Update)"})
	}
	out = append(out, Check{Group: g, Name: "Packet capture", Level: CheckOK,
		Detail: "libpcap — crafted TCP segments, no network interface of its own"})

	listening := strings.EqualFold(strings.TrimSpace(l.Mode), "listen")
	out = append(out, pckPathCheck(g, l, listening, h))
	out = append(out, pckGuardCheck(g, l, h))
	return out
}

// pckPathCheck finds the way out the engine would use: the interface, this
// server's address, and — on the dialling side — the gateway's hardware address.
func pckPathCheck(g string, l config.L3Config, listening bool, h pckHost) Check {
	name := "Gateway"
	var peer net.IP
	if listening {
		// The listening side has no peer to route toward; it learns each
		// client's next hop from what arrives, so there is no gateway to find.
		name = "Network path"
	} else {
		host, _, err := net.SplitHostPort(strings.TrimSpace(l.Addr))
		if err != nil || host == "" {
			return Check{Group: g, Name: name, Level: CheckFail,
				Detail: fmt.Sprintf("addr %q is not host:port", l.Addr),
				Fix:    "fix it with Manage → Manage Tunnels → Edit"}
		}
		if peer = net.ParseIP(host); peer == nil {
			ips, err := h.resolve(host)
			if err != nil {
				return Check{Group: g, Name: name, Level: CheckFail,
					Detail: host + " does not resolve: " + err.Error(),
					Fix:    "give the kharej server's IPv4 address, or fix the DNS"}
			}
			for _, ip := range ips {
				if v4 := ip.To4(); v4 != nil {
					peer = v4
					break
				}
			}
			if peer == nil {
				return Check{Group: g, Name: name, Level: CheckFail,
					Detail: host + " has no IPv4 address, and PCK carries IPv4 only",
					Fix:    "give the kharej server's IPv4 address"}
			}
		} else if peer.To4() == nil {
			return Check{Group: g, Name: name, Level: CheckFail,
				Detail: host + " is an IPv6 address, and PCK carries IPv4 only",
				Fix:    "give the kharej server's IPv4 address or a domain"}
		}
	}
	path, err := h.path(peer, l.PckInterface, l.PckGatewayMAC)
	if err != nil {
		fix := "set pck_interface and pck_gateway_mac in the tunnel's file if the automatic choice is wrong"
		if strings.Contains(err.Error(), "neighbour table") {
			fix = "make the gateway answer ARP (ping it), or set pck_gateway_mac to its address"
		}
		return Check{Group: g, Name: name, Level: CheckFail, Detail: err.Error(), Fix: fix}
	}
	detail := path.Iface + " (" + path.LocalIP.String() + ")"
	switch {
	case listening:
		detail += ", the next hop is learned from each client's segments"
	case path.NextHop != nil:
		detail += ", gateway " + path.NextHop.String()
	case !path.Ethernet:
		detail += ", no link-layer header, so no gateway is needed"
	}
	return Check{Group: g, Name: name, Level: CheckOK, Detail: detail}
}

// pckGuardCheck reads the firewall guard from the tables.
func pckGuardCheck(g string, l config.L3Config, h pckHost) Check {
	const name = "Firewall guard"
	st := h.guard(l.Token)
	switch {
	case !st.Iptables:
		return Check{Group: g, Name: name, Level: CheckFail,
			Detail: "iptables is not installed, so nothing stops the kernel answering the tunnel's segments " +
				"with RSTs; the flow drops at the first stateful device on the path",
			Fix: "install iptables (apt install iptables), then restart the tunnel"}
	case st.Err != nil:
		return Check{Group: g, Name: name, Level: CheckWarn,
			Detail: "could not read the firewall rules: " + st.Err.Error(),
			Fix:    "run the diagnosis as root"}
	case st.Rules == 0:
		return Check{Group: g, Name: name, Level: CheckFail,
			Detail: "none of the tunnel's firewall rules is installed — they are put in when the tunnel starts " +
				"and taken out when it stops, so the tunnel is not running or could not install them",
			Fix: "restart the tunnel and read its log; it needs root"}
	case st.Rules < network.PckGuardWant:
		return Check{Group: g, Name: name, Level: CheckWarn,
			Detail: fmt.Sprintf("%d of %d firewall rules are installed", st.Rules, network.PckGuardWant),
			Fix:    "restart the tunnel to put the missing ones back"}
	}
	return Check{Group: g, Name: name, Level: CheckOK,
		Detail: "the kernel's RSTs to the tunnel are dropped, and its segments are kept out of connection tracking"}
}
