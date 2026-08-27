//go:build linux

package l3

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/tunnel/mssclamp"
	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
	wgtun "golang.zx2c4.com/wireguard/tun"
)

// The TUN device.
//
// Opening one is a single ioctl on /dev/net/tun, so it is done directly with
// x/sys/unix rather than through a library: the whole of the interaction is
// the twenty lines below, and pulling in a dependency for it would be a poor
// trade against this tree's one-binary, few-dependencies habit.
//
// Configuring the interface afterwards — address, MTU, link up — goes through
// the ip(8) command rather than netlink. Netlink would avoid the fork, but it
// is a great deal of code to write and get right for something that happens
// three times at startup, and this tree already shells out for iptables in the
// spoof carrier for the same reason.
//
// IFF_NO_PI is what keeps the read path simple: without it every packet
// arrives behind a four-byte protocol prefix that would have to be stripped on
// read and prepended on write. With it, what is read from the device is
// exactly an IP packet, which is exactly what the encapsulation expects.

// tunDevice is an open TUN interface.
type tunDevice struct {
	dev  wgtun.Device
	name string
	mtu  int

	// The batch machinery, allocated once. Read needs somewhere to put the
	// virtio header the kernel prefixes each read with when offload is on, and
	// Write needs room in front of each packet for the same thing on the way
	// out — so both go through a staging buffer rather than the caller's.
	rbuf  []byte
	wbufs [][]byte
	wbuf  []byte

	// What Close has to undo. A firewall rule outlives the interface it names,
	// so it has to be taken back out by whoever put it in.
	mssClamp int
	log      *logrus.Logger

	// Rate limiting for the too-many-segments report; see Read.
	segReport reportEvery
}

func (t *tunDevice) Name() string { return t.name }
func (t *tunDevice) MTU() int     { return t.mtu }

// SetMTU changes the interface's MTU while it is up, and re-clamps to match.
//
// The clamp is derived from the MTU, so leaving it at the old value after a
// change would leave TCP negotiating segments the interface can no longer
// carry — the exact fault the clamp exists to prevent, reintroduced by the
// thing that was meant to fix it.
func (t *tunDevice) SetMTU(mtu int) error {
	if err := run("ip", "link", "set", "dev", t.name, "mtu", strconv.Itoa(mtu)); err != nil {
		return err
	}
	mssclamp.Remove("l3", t.name, t.mtu, t.mssClamp)
	t.mtu = mtu
	mssclamp.Apply("l3", t.name, mtu, t.mssClamp, t.log)
	return nil
}

// run executes one ip(8) command, naming what was attempted on failure.
func run(cmd string, args ...string) error {
	out, err := exec.Command(cmd, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("l3: %s %s: %w: %s",
			cmd, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Read returns up to BatchSize packets from one syscall.
//
// With IFF_VNET_HDR the kernel hands over a whole segmentation-offload run and
// the library splits it into segments for us. Each buffer must be tunReadBuf
// long, because a segment is sized by the sender and not by this interface —
// see the constant for what a short one costs.
//
// A run that splits into more segments than there are buffers is a short read,
// not a failure. The library reports it as one, and taking it at its word is
// what put a tunnel in the field into a restart loop: every read of a coalesced
// run of small datagrams returned this, the pump treated it as the device
// failing, and the tunnel tore down and rebuilt itself every few seconds. The
// packets that did fit are perfectly good, so they are returned; the rest are
// lost, which is what a full receive queue does to a packet in any case and is
// nothing like as bad as dropping the tunnel.
func (t *tunDevice) Read(bufs [][]byte, sizes []int) (int, error) {
	n, err := t.dev.Read(bufs, sizes, 0)
	kept, overflow := segmentOverflow(n, err)
	if !overflow {
		return n, err
	}
	if seen, say := t.segReport.allow(time.Now()); say {
		t.log.Warnf("l3: %s returned a run of more than %d segments and the rest were dropped "+
			"(%d reads so far). That is the kernel coalescing many small packets into one; "+
			"the tunnel keeps running.", t.name, len(bufs), seen)
	}
	return kept, nil
}

// Write injects packets into the kernel's routing.
//
// Each packet is staged behind virtioOffset bytes of room, which is where the
// library writes the header the kernel wants. Handing it several at once is
// what lets it coalesce them into fewer writes.
func (t *tunDevice) Write(bufs [][]byte) (int, error) {
	if len(bufs) == 0 {
		return 0, nil
	}
	t.wbufs = t.wbufs[:0]
	off := 0
	for _, p := range bufs {
		end := off + virtioOffset + len(p)
		if end > len(t.wbuf) {
			// More than the staging buffer holds: write what is staged and
			// send the rest on its own rather than truncating.
			break
		}
		copy(t.wbuf[off+virtioOffset:], p)
		t.wbufs = append(t.wbufs, t.wbuf[off:end])
		off = end
	}
	if len(t.wbufs) == 0 {
		return 0, nil
	}
	return t.dev.Write(t.wbufs, virtioOffset)
}

// BatchSize is the most packets one Read or Write may move.
func (t *tunDevice) BatchSize() int { return t.dev.BatchSize() }

// Close removes what the device installed and then destroys it. The clamp goes
// first: once the file is closed the interface is gone, and a rule naming an
// interface that no longer exists is exactly the litter that accumulated on the
// pck carrier in the field.
func (t *tunDevice) Close() error {
	mssclamp.Remove("l3", t.name, t.mtu, t.mssClamp)
	return t.dev.Close()
}

// virtioOffset is the room left in front of every packet for the virtio-net
// header the kernel prefixes when segmentation offload is on. Ten bytes is what
// the header is; the buffers carry it whether or not offload ends up enabled,
// because the device decides that at open time and the cost is ten bytes.
const virtioOffset = 10

// tunStageSize is the staging buffer the write path copies into. Big enough for
// a full batch of MTU-sized packets with their offsets, so a busy write is one
// syscall rather than one per packet.
const tunStageSize = 1 << 20

// openTUNTuned creates a TUN interface and brings it up with the given address
// and MTU.
//
// The device itself comes from wireguard-go's tun package rather than a raw
// ioctl on /dev/net/tun, which is what this used to do. What that buys is the
// batched read and write above: with IFF_VNET_HDR the kernel will hand over a
// whole segmentation-offload run in one syscall, and the library does the
// splitting and the coalescing — several hundred lines of checksum and
// sequence-number fixups that are not worth writing twice.
//
// Everything after the device is unchanged and still goes through ip(8): the
// addresses, the MTU, the queue length, the queueing discipline and the MSS
// clamp. wireguard's package creates a device; it has no opinion about how the
// interface is configured, which suits this exactly.
func openTUNTuned(name, localIP, peerIP string, mtu, mssClamp, txQueueLen int, qdisc string, log *logrus.Logger) (*tunDevice, error) {
	if log == nil {
		log = logrus.StandardLogger()
	}
	if name == "" {
		name = defaultIfaceName
	}
	if len(name) >= unix.IFNAMSIZ {
		return nil, fmt.Errorf("l3: interface name %q is longer than %d characters", name, unix.IFNAMSIZ-1)
	}

	wdev, err := wgtun.CreateTUN(name, mtu)
	if err != nil {
		if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) {
			return nil, fmt.Errorf("l3: creating the TUN interface %q: %w "+
				"(a layer-3 tunnel needs root or CAP_NET_ADMIN)", name, err)
		}
		if errors.Is(err, unix.ENOENT) {
			return nil, fmt.Errorf("l3: creating the TUN interface %q: %w "+
				"(/dev/net/tun is missing — the tun module may not be loaded)", name, err)
		}
		return nil, fmt.Errorf("l3: creating the TUN interface %q: %w", name, err)
	}

	// The kernel may hand back a different name than the one asked for, which
	// is what happens when the name carried a %d template.
	actual, err := wdev.Name()
	if err != nil || actual == "" {
		actual = name
	}

	dev := &tunDevice{
		dev:       wdev,
		name:      actual,
		mtu:       mtu,
		mssClamp:  mssClamp,
		log:       log,
		wbufs:     make([][]byte, 0, wdev.BatchSize()),
		wbuf:      make([]byte, tunStageSize),
		segReport: reportEvery{every: time.Minute},
	}

	if err := configureInterface(actual, localIP, peerIP, mtu, txQueueLen); err != nil {
		dev.Close()
		return nil, err
	}
	applyQdisc(actual, qdisc, log)

	// After the interface is up, so the rule names something that exists.
	mssclamp.Apply("l3", actual, mtu, mssClamp, log)
	return dev, nil
}

// configureInterface gives the new interface its address and MTU and brings it
// up.
//
// Two address forms are supported, because the two describe different things.
// A prefix shorter than /32 — "10.10.0.1/30" — installs a subnet route, and
// the peer is reached because it falls inside that subnet. A bare address with
// an explicit peer installs a point-to-point route to exactly one host, which
// is the tighter description of what this tunnel actually is.
func configureInterface(name, localIP, peerIP string, mtu, txQueueLen int) error {
	addrArgs := []string{"addr", "add", localIP, "dev", name}
	if !strings.Contains(localIP, "/") {
		if peerIP == "" {
			return fmt.Errorf("l3: local_ip %q has no prefix length, so peer_ip is required", localIP)
		}
		addrArgs = []string{"addr", "add", localIP, "peer", peerIP, "dev", name}
	}

	steps := [][]string{
		addrArgs,
		{"link", "set", "dev", name, "mtu", strconv.Itoa(mtu)},
		// The queue between the kernel and this process.
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
	for _, args := range steps {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("l3: ip %s: %w: %s",
				strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}

	// A /30 or wider gives the peer a subnet route already. A prefix that
	// covers only this host does not, so the route has to be stated.
	if peerIP != "" && needsExplicitPeerRoute(localIP) {
		args := []string{"route", "replace", peerIP, "dev", name}
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("l3: ip %s: %w: %s",
				strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
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

// applyQdisc puts a queueing discipline on the interface.
//
// This is the setting that decides jitter, and it is why the queue above can be
// deep without the latency that usually comes with one. A plain FIFO — or fq,
// which paces but does not manage depth — lets a burst fill the queue and every
// packet behind it waits for the whole thing to drain. fq_codel watches how
// long packets are actually sitting there and starts dropping when the delay
// climbs, so the sender backs off before the queue becomes latency.
//
// Best effort. tc is not always installed, and a tunnel with the kernel's
// default qdisc works — it simply has worse latency under load, which is
// exactly the thing this is here to avoid, so a failure is worth a line in the
// log rather than silence.
func applyQdisc(name, qdisc string, log *logrus.Logger) {
	if qdisc == "" || qdisc == "none" {
		return
	}
	out, err := exec.Command("tc", "qdisc", "replace", "dev", name, "root", qdisc).CombinedOutput()
	if err != nil {
		log.Warnf("l3: could not set the %s queueing discipline on %s (%v: %s) — "+
			"the tunnel works, but latency under load will be worse",
			qdisc, name, err, strings.TrimSpace(string(out)))
		return
	}
	log.Infof("l3: %s is queueing with %s", name, qdisc)
}
