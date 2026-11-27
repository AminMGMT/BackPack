//go:build linux

package network

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/net/icmp"
	"golang.org/x/sys/unix"
)

// The layer-3 xdi carrier sends packets whole: the don't-fragment bit and no
// fragmenting here, so the tunnel's MTU probe sees a size the path cannot
// carry instead of having it split and delivered (see sendWhole). KCP over
// xdi keeps the kernel's default.
func TestTheLayer3XdiCarrierNeverFragmentsLocally(t *testing.T) {
	for _, c := range []struct {
		name, addr      string
		level, opt, val int
	}{
		{"ICMP", "127.0.0.1", unix.IPPROTO_IP, unix.IP_MTU_DISCOVER, unix.IP_PMTUDISC_PROBE},
		{"ICMPv6", "::1", unix.IPPROTO_IPV6, unix.IPV6_MTU_DISCOVER, unix.IPV6_PMTUDISC_PROBE},
	} {
		t.Run(c.name, func(t *testing.T) {
			conn, _, err := NewXdiPacketConn(false, "a-whole-packets-token-0123456789", c.addr, 0)
			if errors.Is(err, os.ErrPermission) {
				t.Skip("needs CAP_NET_RAW (run as root or in a user namespace)")
			}
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if got := mtuDiscover(t, conn.(*icmpConn), c.level, c.opt); got != c.val {
				t.Fatalf("the layer-3 carrier's MTU discovery is %d, want %d (probe: DF, no local fragments)", got, c.val)
			}

			kcpSide, err := newICMPClientConn("a-whole-packets-token-0123456789", 0, c.name == "ICMPv6")
			if err != nil {
				t.Fatal(err)
			}
			defer kcpSide.Close()
			if got := mtuDiscover(t, kcpSide.(*icmpConn), c.level, c.opt); got == c.val {
				t.Fatalf("KCP's xdi socket refuses oversized packets too; kcp-go would end its session on the first")
			}
		})
	}
}

func mtuDiscover(t *testing.T, c *icmpConn, level, opt int) int {
	t.Helper()
	got := -1
	icmpSocketControl(c.pc.(*icmp.PacketConn), func(fd int) {
		v, err := unix.GetsockoptInt(fd, level, opt)
		if err != nil {
			t.Fatal(err)
		}
		got = v
	})
	return got
}
