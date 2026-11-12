//go:build linux

package l3

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/tunnel/mssclamp"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
)

// runKernelGRE is one generation of a kernel GRE tunnel: the link exists for
// exactly as long as this runs. See kernelgre.go for why this is a path of its
// own rather than a carrier.
func (t *Tunnel) runKernelGRE(ctx context.Context) error {
	remote, err := resolveGREEndpoint(ctx, t.cfg.Addr, t.cfg.Carrier)
	if err != nil {
		return err
	}
	local := net.ParseIP(t.cfg.GRELocal)
	if local == nil {
		if local, err = GRELocalToward(remote); err != nil {
			return err
		}
	}
	mtu := t.cfg.MTU
	if mtu == 0 {
		mtu = greMTUFor(egressMTU(local), t.cfg.Carrier, t.cfg.GREKey)
	}

	spec := greLinkSpec{Name: t.cfg.Iface, Carrier: t.cfg.Carrier, Local: local, Remote: remote, Key: t.cfg.GREKey}
	link, err := openGRELink(spec, mtu, t.cfg.MSSClamp, t.cfg.TxQueueLen,
		t.cfg.LocalIP, t.cfg.PeerIP, t.log)
	if err != nil {
		return err
	}
	name := spec.Name
	t.gre.iface.Store(&name)
	t.setCurrentMTU(mtu)
	defer func() {
		last, _ := readGRECounters(name)
		t.gre.retire(last)
		link.Close()
		metrics.ClearPeer()
	}()

	accept := installGREAccept(spec, t.log)
	defer accept.remove()

	key := "no key"
	if spec.Key != 0 {
		key = fmt.Sprintf("key %d", spec.Key)
	}
	t.log.Infof("l3: %s up on %s, %s → %s (%s), mtu %d. Kernel GRE is IP protocol 47 and unencrypted: "+
		"whatever crosses it is in the clear unless it is encrypted itself", spec.Carrier, name, local, remote, key, mtu)

	peerInner := net.ParseIP(hostPart(t.cfg.PeerIP))
	localInner := net.ParseIP(hostPart(t.cfg.LocalIP))
	pinger, err := openGREPinger(localInner, peerInner, func() {
		t.gre.lastAnswer.Store(time.Now().UnixNano())
		metrics.ReportPeer(remote.String())
	})
	if err != nil {
		// The link works without it; only the screens are blind.
		t.log.Warnf("l3: cannot ping the other end across %s (%v) — the tunnel carries traffic, "+
			"but nothing can tell whether the other end is there", name, err)
	} else {
		defer pinger.Close()
	}

	// The probe ends with the generation, which has to be ended before it is
	// waited for: a generation that returns because its link was removed has a
	// caller whose context is still live.
	genCtx, endGeneration := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { endGeneration(); wg.Wait() }()
	if pinger != nil {
		wg.Add(1)
		go func() { defer wg.Done(); t.greProbeLoop(genCtx, link, pinger) }()
	}

	reported := false
	tick := time.NewTicker(greKeepalive)
	defer tick.Stop()
	for {
		if !linkExists(name) {
			return errGREGone
		}
		if pinger != nil {
			pinger.Ping(genCtx, 0, grePingTimeout)
		}
		switch up := t.gre.up(time.Now()); {
		case up && !reported:
			t.log.Infof("l3: the other end answers across %s (%s)", name, peerInner)
			reported = true
		case !up && reported:
			t.log.Warnf("l3: the other end has not answered across %s for %s — check that its tunnel is "+
				"running and that nothing between the two servers drops IP protocol 47", name, greSilentAfter)
			metrics.ClearPeer()
			reported = false
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// greProbeLoop measures what the path carries once the other end answers, and
// again every probeEvery, the way the session carriers' probe does. Each size is
// a ping with the don't-fragment bit, so a size the path cannot carry is lost
// out on the path exactly as a large data packet would be.
//
// It searches up to the interface's current MTU and lowers it when the path
// carries less; it never raises it, because a size above the interface's MTU
// cannot be sent to test. A restart starts again from the figure the route's
// interface gives.
func (t *Tunnel) greProbeLoop(ctx context.Context, link *greLink, p *grePinger) {
	if !t.cfg.AutoMTU || !p.v4 {
		return
	}
	for !t.gre.up(time.Now()) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(t.probe.settle):
	}
	for {
		current := link.MTU()
		found, ok := greSearchMTU(minMTU, current, func(size int) bool {
			for attempt := 0; attempt < t.probe.attempts; attempt++ {
				if ctx.Err() != nil {
					return false
				}
				if p.Ping(ctx, size, t.probe.timeout) {
					return true
				}
			}
			return false
		})
		switch {
		case ctx.Err() != nil:
			return
		case !ok:
			t.log.Debugf("l3: could not measure the path across %s — keeping mtu %d", link.name, current)
		case found < current:
			if err := link.SetMTU(found); err != nil {
				t.log.Warnf("l3: measured the path at %d but could not set %s: %v", found, link.name, err)
				break
			}
			t.setCurrentMTU(found)
			t.log.Warnf("l3: the path only carries %d bytes, not %d — %s lowered to %d. "+
				"Large transfers would otherwise stall while ping kept working.", found, current, link.name, found)
			if found < probeWarnBelow {
				t.log.Warnf("l3: %d is below the %d IPv6 requires, so IPv6 will not cross this tunnel",
					found, probeWarnBelow)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(t.probe.every):
		}
	}
}

// greLink is an open kernel GRE link.
type greLink struct {
	name     string
	mssClamp int
	log      *logrus.Logger

	mu  sync.Mutex
	mtu int
}

// openGRELink creates the link and configures it the way the TUN device is —
// address, MTU, queue length, link up, MSS clamp — except for the queueing
// discipline, which it leaves as the kernel made it. A link of the same name
// left by an earlier run of this tunnel — a crash, a kill -9 — is replaced
// rather than refused, so a restart always comes back.
//
// # No qdisc on a GRE link
//
// The kernel creates a GRE link with noqueue, and that is the right answer
// rather than an omission. A qdisc only does anything where packets wait, and
// none wait here: a GRE link never stops its queue, it hands each packet
// straight to the route out, and the queue that fills under load is that
// interface's, where its own qdisc already manages it. The TUN device is the
// opposite case — drained by this process, it backs up, and fq_codel there is
// what keeps a full queue from turning into latency — which is why the preset's
// qdisc stays on the TUN path.
//
// What one costs on a GRE link is a lock: every packet of every connection
// takes the qdisc's root lock to be queued and dequeued again at once, and the
// CPUs sending in parallel queue up on it. Measured between two network
// namespaces over a veth (CPU-bound, so this is the cost of the lock, not a
// WAN prediction), bulk TCP across the link, fq_codel against noqueue,
// median of three: 4 streams 43.0 → 50.8 Gb/s and 16 streams 29.6 → 38.0
// Gb/s iran→kharej, 16 streams 27.1 → 31.4 the other way; one stream the same.
// With noqueue the link carries what the bare veth does (50.7 at 4 streams).
func openGRELink(s greLinkSpec, mtu, mssClamp, txQueueLen int, localIP, peerIP string,
	log *logrus.Logger) (*greLink, error) {
	if log == nil {
		log = logrus.StandardLogger()
	}
	if len(s.Name) >= unix.IFNAMSIZ {
		return nil, fmt.Errorf("l3: interface name %q is longer than %d characters", s.Name, unix.IFNAMSIZ-1)
	}
	// The kernel loads the module itself on the first link of its type where it
	// is allowed to. Asking first costs nothing and covers a host where that
	// autoload is off; when it fails, the error from the add below says what
	// to do.
	_ = exec.Command("modprobe", greModule(s.Carrier)).Run()

	if err := removeStaleGRE(s.Name); err != nil {
		return nil, err
	}
	if out, err := exec.Command("ip", greLinkAddArgs(s)...).CombinedOutput(); err != nil {
		return nil, explainGREAddFailure(s, string(out), err)
	}
	link := &greLink{name: s.Name, mssClamp: mssClamp, log: log, mtu: mtu}
	steps, err := interfaceSteps(s.Name, localIP, peerIP, mtu, txQueueLen)
	if err == nil {
		err = runIPSteps(steps)
	}
	if err != nil {
		_ = exec.Command("ip", greLinkDelArgs(s.Name)...).Run()
		return nil, err
	}
	mssclamp.Apply("l3", s.Name, mtu, mssClamp, log)
	return link, nil
}

// MTU is what the link is set to now.
func (l *greLink) MTU() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.mtu
}

// SetMTU changes the link's MTU and re-clamps to match, for the same reason
// tunDevice.SetMTU does.
func (l *greLink) SetMTU(mtu int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := run("ip", "link", "set", "dev", l.name, "mtu", strconv.Itoa(mtu)); err != nil {
		return err
	}
	mssclamp.Remove("l3", l.name, l.mtu, l.mssClamp)
	l.mtu = mtu
	mssclamp.Apply("l3", l.name, mtu, l.mssClamp, l.log)
	return nil
}

// Close takes the clamp out and the link down, in that order: a rule naming an
// interface that no longer exists is litter.
func (l *greLink) Close() {
	l.mu.Lock()
	mtu := l.mtu
	l.mu.Unlock()
	mssclamp.Remove("l3", l.name, mtu, l.mssClamp)
	_ = exec.Command("ip", greLinkDelArgs(l.name)...).Run()
}

// removeStaleGRE deletes a GRE link under this tunnel's name, and refuses to
// touch anything else that has it.
func removeStaleGRE(name string) error {
	raw, err := os.ReadFile("/sys/class/net/" + name + "/type")
	if err != nil {
		return nil // nothing there
	}
	kind, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if kind != arphrdIPGRE && kind != arphrdIP6GRE {
		return fmt.Errorf("l3: an interface named %s already exists and is not a GRE link — another "+
			"tunnel or program holds that name; give this tunnel another iface", name)
	}
	if out, err := exec.Command("ip", greLinkDelArgs(name)...).CombinedOutput(); err != nil {
		return fmt.Errorf("l3: removing the %s an earlier run left behind: %w: %s",
			name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runIPSteps runs ip(8) commands in order, naming the one that failed.
func runIPSteps(steps [][]string) error {
	for _, args := range steps {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("l3: ip %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// linkExists reports whether an interface of this name is present.
func linkExists(name string) bool {
	_, err := os.Stat("/sys/class/net/" + name)
	return err == nil
}

// readGRECounters reads a link's traffic counters.
func readGRECounters(name string) (greCounters, bool) {
	dir := "/sys/class/net/" + name + "/statistics/"
	read := func(file string) (uint64, bool) {
		raw, err := os.ReadFile(dir + file)
		if err != nil {
			return 0, false
		}
		v, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		return v, err == nil
	}
	var c greCounters
	var ok bool
	if c.bytesIn, ok = read("rx_bytes"); !ok {
		return greCounters{}, false
	}
	c.bytesOut, _ = read("tx_bytes")
	c.packetsIn, _ = read("rx_packets")
	c.packetsOut, _ = read("tx_packets")
	rxd, _ := read("rx_dropped")
	txd, _ := read("tx_dropped")
	c.dropped = rxd + txd
	return c, true
}

// resolveGREEndpoint is the other server's address, of the carrier's family.
func resolveGREEndpoint(ctx context.Context, host, carrier string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip, nil
	}
	network := "ip4"
	if carrier == CarrierIP6GRE {
		network = "ip6"
	}
	rctx, cancel := context.WithTimeout(ctx, carrierResolveTimeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(rctx, network, host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("l3: resolving %s to an %s address for the GRE link: %v", host, network, err)
	}
	return ips[0], nil
}

// egressMTU is the MTU of the interface that holds local, or zero.
func egressMTU(local net.IP) int {
	ifaces, err := net.Interfaces()
	if err != nil {
		return 0
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(local) {
				return iface.MTU
			}
		}
	}
	return 0
}

// hostPart drops a prefix length.
func hostPart(addr string) string {
	host, _, _ := strings.Cut(strings.TrimSpace(addr), "/")
	return host
}

// Letting GRE in.
//
// A host firewall that drops what it does not know drops protocol 47, and the
// GRE link comes up, holds its addresses and receives nothing. xdi has the same
// problem with ICMP and answers it the same way: one ACCEPT rule for exactly
// this tunnel's packets — protocol 47 from the other server — inserted while it
// runs and taken out when it stops.

type greAccept struct {
	tool   string
	rule   []string
	active bool
}

// installGREAccept puts the rule in, or adopts one a crashed run left. Where it
// cannot, it says what rule to add by hand. Nil when there is no iptables to
// ask, which leaves the firewall to whoever manages it.
func installGREAccept(s greLinkSpec, log *logrus.Logger) *greAccept {
	tool := "iptables"
	if s.Carrier == CarrierIP6GRE {
		tool = "ip6tables"
	}
	if _, err := exec.LookPath(tool); err != nil {
		return nil
	}
	a := &greAccept{tool: tool, rule: greAcceptRule(s.Name, s.Remote)}
	if exec.Command(tool, append([]string{"-C"}, a.rule...)...).Run() == nil {
		a.active = true
		return a
	}
	if out, err := exec.Command(tool, append([]string{"-I"}, a.rule...)...).CombinedOutput(); err != nil {
		log.Warnf("l3: could not let GRE from %s in through the firewall (%v: %s). If this server's "+
			"firewall drops IP protocol 47 the tunnel cannot come up — allow it: %s -I INPUT -p 47 -s %s -j ACCEPT",
			s.Remote, err, strings.TrimSpace(string(out)), tool, s.Remote)
		return a
	}
	a.active = true
	return a
}

// remove takes the rule out. Safe on nil and on a rule never installed.
func (a *greAccept) remove() {
	if a == nil || !a.active {
		return
	}
	a.active = false
	_ = exec.Command(a.tool, append([]string{"-D"}, a.rule...)...).Run()
}

// Pinging the other end across the link.

// grePinger sends echo requests to the other end's tunnel address from a raw
// ICMP socket bound to this end's, and hears the replies. The socket is raw, so
// the identifier is this one's own and the kernel does not rewrite it.
type grePinger struct {
	conn     *net.IPConn
	v4       bool
	proto    int
	id       int
	peer     *net.IPAddr
	answered func()

	seq     atomic.Uint32
	mu      sync.Mutex
	waiting map[int]chan struct{}
	done    chan struct{}
}

func openGREPinger(localInner, peerInner net.IP, answered func()) (*grePinger, error) {
	if localInner == nil || peerInner == nil {
		return nil, fmt.Errorf("the tunnel addresses are not plain IP addresses")
	}
	v4 := peerInner.To4() != nil
	network, proto := "ip4:icmp", 1
	if !v4 {
		network, proto = "ip6:ipv6-icmp", 58
	}
	conn, err := net.ListenIP(network, &net.IPAddr{IP: localInner})
	if err != nil {
		return nil, err
	}
	// The don't-fragment bit, without the kernel's cached path MTU in the way:
	// what is sent is exactly the size asked for, and a size the path cannot
	// carry is lost on the path rather than split or refused here.
	if raw, err := conn.SyscallConn(); err == nil {
		_ = raw.Control(func(fd uintptr) {
			if v4 {
				_ = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MTU_DISCOVER, unix.IP_PMTUDISC_PROBE)
			} else {
				_ = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_MTU_DISCOVER, unix.IPV6_PMTUDISC_PROBE)
			}
		})
	}
	p := &grePinger{
		conn: conn, v4: v4, proto: proto, id: rand.Intn(0xffff) + 1,
		peer: &net.IPAddr{IP: peerInner}, answered: answered,
		waiting: map[int]chan struct{}{}, done: make(chan struct{}),
	}
	go p.read()
	return p, nil
}

// Close stops the reader.
func (p *grePinger) Close() {
	p.conn.Close()
	<-p.done
}

// Ping sends one echo of size bytes in all — the IP packet, headers included —
// and reports whether it was answered in time. Zero is a small ping.
func (p *grePinger) Ping(ctx context.Context, size int, timeout time.Duration) bool {
	header := 28
	typ := icmp.Type(ipv4.ICMPTypeEcho)
	if !p.v4 {
		header, typ = 48, ipv6.ICMPTypeEchoRequest
	}
	payload := 16
	if size > 0 {
		payload = size - header
	}
	if payload < 8 {
		payload = 8
	}
	seq := int(p.seq.Add(1) & 0xffff)
	msg := icmp.Message{Type: typ, Body: &icmp.Echo{ID: p.id, Seq: seq, Data: make([]byte, payload)}}
	wire, err := msg.Marshal(nil)
	if err != nil {
		return false
	}
	ch := make(chan struct{})
	p.mu.Lock()
	p.waiting[seq] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.waiting, seq)
		p.mu.Unlock()
	}()
	if _, err := p.conn.WriteTo(wire, p.peer); err != nil {
		// EMSGSIZE for a size above the interface MTU: does not fit.
		return false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

func (p *grePinger) read() {
	defer close(p.done)
	buf := make([]byte, 1<<16)
	for {
		n, from, err := p.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		if ip, ok := from.(*net.IPAddr); !ok || !ip.IP.Equal(p.peer.IP) {
			continue
		}
		msg, err := icmp.ParseMessage(p.proto, buf[:n])
		if err != nil {
			continue
		}
		if msg.Type != ipv4.ICMPTypeEchoReply && msg.Type != ipv6.ICMPTypeEchoReply {
			continue
		}
		echo, ok := msg.Body.(*icmp.Echo)
		if !ok || echo.ID != p.id {
			continue
		}
		p.answered()
		p.mu.Lock()
		if ch, ok := p.waiting[echo.Seq]; ok {
			close(ch)
			delete(p.waiting, echo.Seq)
		}
		p.mu.Unlock()
	}
}

// CleanupGRE takes away what a GRE tunnel's engine leaves behind only when it
// is killed rather than stopped: its link, and its firewall rule. Delete calls
// it after the service is down, so a name and a rule are never left to the
// next tunnel that takes the interface. It touches a link under that name only
// when it is a GRE link.
func CleanupGRE(iface, carrier, addr string) {
	if iface == "" {
		iface = defaultIfaceName
	}
	_ = removeStaleGRE(iface)
	if remote := net.ParseIP(strings.Trim(addr, "[]")); remote != nil {
		tool := "iptables"
		if carrier == CarrierIP6GRE {
			tool = "ip6tables"
		}
		rule := greAcceptRule(iface, remote)
		for exec.Command(tool, append([]string{"-D"}, rule...)...).Run() == nil {
			// A rule a crash adopted and a crash after that inserted again.
		}
	}
}
