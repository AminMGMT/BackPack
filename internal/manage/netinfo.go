package manage

import (
	"context"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// httpClientV4 and httpClientV6 force the IP family so we can detect each
// address independently.
func ipClient(network string) *http.Client {
	dialer := &net.Dialer{Timeout: 4 * time.Second}
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
		},
	}
}

func fetchIP(network string) string {
	client := ipClient(network)
	urls := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://api.ip.sb/ip",
		"https://ipinfo.io/ip",
		"https://icanhazip.com",
	}
	for _, url := range urls {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
		resp.Body.Close()
		ip := strings.TrimSpace(string(body))
		if net.ParseIP(ip) != nil {
			return ip
		}
	}
	return ""
}

// Where a public address was decided from. The panel shows this, because
// "which IP is this server" has two possible answers on a surprising number of
// hosts and an unlabelled wrong one is what sent this bug report.
const (
	SourceInterface = "interface" // an address this machine actually holds
	SourceEcho      = "echo"      // what an address echo service saw us come from
)

// PublicIPv4 returns the server's public IPv4 address, or "-" if unavailable.
func PublicIPv4() string { ip, _ := PublicIPv4Detail(); return ip }

// PublicIPv6 returns the server's public IPv6 address, or "-" if unavailable.
func PublicIPv6() string { ip, _ := PublicIPv6Detail(); return ip }

// PublicIPv4Detail returns the address and where it was taken from.
//
// The machine's own interfaces are asked first, and the echo services only when
// it holds no routable address of its own.
//
// It used to be the echo services alone, which answer a different question:
// they report the address this host's *outbound* traffic appears to come from.
// Those agree on an ordinary VPS and part company whenever egress leaves by
// another path — behind NAT or a cloud gateway, on a multi-homed box whose
// default route is not the interface users arrive on, and most sharply on a
// server that routes its own traffic through another tunnel, where the echo
// reports the far end of that tunnel. The panel then ran its geo lookup on that
// address and reported the server as being in another country entirely, with
// another operator's name against it.
//
// The interface answer is also free and instant, so the common case no longer
// waits on a third-party HTTP call at all.
func PublicIPv4Detail() (ip, source string) { return publicAddr(true) }

// PublicIPv6Detail is PublicIPv4Detail for IPv6.
func PublicIPv6Detail() (ip, source string) { return publicAddr(false) }

func publicAddr(v4 bool) (string, string) {
	if local := localPublicIPs(v4); len(local) > 0 {
		return pickLocal(local), SourceInterface
	}
	network := "tcp6"
	if v4 {
		network = "tcp4"
	}
	if ip := fetchIP(network); ip != "" {
		return ip, SourceEcho
	}
	return "-", ""
}

// localPublicIPs returns the globally routable addresses bound on this
// machine's interfaces, sorted so the answer does not move between calls.
func localPublicIPs(v4 bool) []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || !globallyRoutable(ipnet.IP) {
			continue
		}
		if isV4 := ipnet.IP.To4() != nil; isV4 != v4 {
			continue
		}
		out = append(out, ipnet.IP.String())
	}
	sort.Strings(out)
	return out
}

// pickLocal chooses between several routable addresses on one machine.
//
// A host with more than one public address has no inherently correct answer, so
// the tunnels decide it: an address a server tunnel is bound to is the one
// clients are told to reach, which makes it the one the panel should call this
// server's. The tunnel list is only read when there is actually a choice to
// make, so the ordinary single-address host never parses a config for this.
func pickLocal(candidates []string) string {
	if len(candidates) == 1 {
		return candidates[0]
	}
	bound := boundServerHosts()
	for _, c := range candidates {
		if bound[c] {
			return c
		}
	}
	return candidates[0]
}

// boundServerHosts is the set of concrete addresses this machine's server-role
// tunnels listen on. A wildcard bind (0.0.0.0, ::) names no address and is
// skipped rather than counted as one.
func boundServerHosts() map[string]bool {
	out := map[string]bool{}
	for _, t := range List() {
		if t.Role != "server" {
			continue
		}
		host, _, err := net.SplitHostPort(t.Addr)
		if err != nil {
			continue
		}
		if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
			out[ip.String()] = true
		}
	}
	return out
}

// globallyRoutable reports whether an address is one the rest of the internet
// could reach — which is what "this server's public IP" means.
//
// net.IP.IsPrivate covers RFC 1918 and RFC 4193 but not carrier-grade NAT
// (100.64.0.0/10), and a CGNAT address is exactly the kind that looks public
// enough to be reported and is not.
func globallyRoutable(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// 100.64.0.0/10, RFC 6598.
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return false
		}
		// 192.0.0.0/24 and the documentation ranges are not somebody's server.
		if v4[0] == 192 && v4[1] == 0 && v4[2] == 0 {
			return false
		}
	}
	return true
}

// PortInUse reports whether a TCP port is already bound locally.
func PortInUse(port string) bool {
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return true
	}
	ln.Close()
	return false
}

// TunnelPortInUse reports whether a tunnel's control port is already taken,
// checking the protocol that transport actually listens on.
//
// addr is the full bind address, not a bare port. A tunnel pinned to one of a
// server's addresses does not contend with a listener on another, and asking
// about ":443" when the tunnel will bind "85.10.11.51:443" answers a question
// nobody asked — it is exactly the refusal the two-address setup is trying to
// get past. A bare port still works and still means every interface.
func TunnelPortInUse(transport, addr string) bool {
	if !strings.Contains(addr, ":") {
		addr = ":" + addr
	}
	// pck is the one transport that binds nothing: its segments are read off
	// the wire rather than delivered by the kernel. It still needs the TCP port
	// to itself, though — a real listener there would receive the tunnel's
	// segments too and answer them, which is exactly the interference the
	// carrier goes to lengths to suppress from the kernel itself.
	if isDatagram(transport) && transport != "pck" {
		return udpAddrInUse(addr)
	}
	return tcpAddrInUse(addr)
}

// tcpAddrInUse and udpAddrInUse are PortInUse and udpPortInUse over a full
// address. The bare-port helpers stay as they are: they have other callers,
// and for those "any interface" is the right question.
func tcpAddrInUse(addr string) bool {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return true
	}
	ln.Close()
	return false
}

func udpAddrInUse(addr string) bool {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return true
	}
	conn.Close()
	return false
}
