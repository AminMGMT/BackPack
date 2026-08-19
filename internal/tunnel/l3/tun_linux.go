//go:build linux

package l3

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unsafe"

	"github.com/backpack/backpack/internal/tunnel/mssclamp"
	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
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
	file *os.File
	name string
	mtu  int

	// What Close has to undo. A firewall rule outlives the interface it names,
	// so it has to be taken back out by whoever put it in.
	mssClamp int
	log      *logrus.Logger
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

// Read returns one IP packet. A TUN device is packet-oriented: each read
// yields exactly one packet, never a partial one and never two.
func (t *tunDevice) Read(p []byte) (int, error) { return t.file.Read(p) }

// Write injects one IP packet into the kernel's routing.
func (t *tunDevice) Write(p []byte) (int, error) { return t.file.Write(p) }

// Close removes what the device installed and then destroys it. The clamp goes
// first: once the file is closed the interface is gone, and a rule naming an
// interface that no longer exists is exactly the litter that accumulated on the
// pck carrier in the field.
func (t *tunDevice) Close() error {
	mssclamp.Remove("l3", t.name, t.mtu, t.mssClamp)
	return t.file.Close()
}

// ifreq is the structure TUNSETIFF expects: a 16-byte interface name followed
// by a union whose first member here is the flags word.
type ifreq struct {
	name  [unix.IFNAMSIZ]byte
	flags uint16
	_     [22]byte
}

// openTUN creates or attaches to a TUN interface and brings it up with the
// given address and MTU.
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

	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		if err == unix.EACCES || err == unix.EPERM {
			return nil, fmt.Errorf("l3: opening /dev/net/tun: %w (a layer-3 tunnel needs root or CAP_NET_ADMIN)", err)
		}
		if err == unix.ENOENT {
			return nil, fmt.Errorf("l3: /dev/net/tun does not exist: %w (the tun module may not be loaded)", err)
		}
		return nil, fmt.Errorf("l3: opening /dev/net/tun: %w", err)
	}

	var req ifreq
	copy(req.name[:], name)
	// IFF_TUN is a layer-3 device; IFF_NO_PI drops the per-packet prefix.
	req.flags = unix.IFF_TUN | unix.IFF_NO_PI

	if _, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(fd),
		uintptr(unix.TUNSETIFF),
		uintptr(unsafe.Pointer(&req)),
	); errno != 0 {
		unix.Close(fd)
		return nil, fmt.Errorf("l3: creating the TUN interface %q: %w", name, errno)
	}

	// The kernel writes back the name it actually used, which differs from the
	// requested one when the name contained a %d template.
	actual := strings.TrimRight(string(req.name[:]), "\x00")
	if actual == "" {
		actual = name
	}

	dev := &tunDevice{
		file:     os.NewFile(uintptr(fd), "/dev/net/tun"),
		name:     actual,
		mtu:      mtu,
		mssClamp: mssClamp,
		log:      log,
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
