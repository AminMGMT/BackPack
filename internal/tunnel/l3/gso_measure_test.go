//go:build linux

package l3

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Is UDP segmentation offload worth putting on the carrier socket?
//
// The TUN device already uses offload on the device side (`vnet_hdr`, see
// tun_linux.go); the carrier socket does not. `UDP_SEGMENT` would let one
// `sendmsg` hand the kernel a run of equal-sized datagrams and have it cut them
// up, which is a smaller number of syscalls again than `sendmmsg` — this
// carrier batches eight at a time, and a GSO write can carry dozens.
//
// That is the argument for. Three things are the argument against, and the
// point of this measurement is to find out which wins:
//
//   - `sendmmsg` already removed seven eighths of the syscalls and bought no
//     measurable throughput on loopback, because on loopback the syscall is not
//     what costs. A second reduction of the same thing should buy the same
//     nothing.
//
//   - GSO needs every segment the same size but the last. Tunnelled IP traffic
//     is full-sized during a transfer and ragged the rest of the time, so the
//     optimisation applies exactly where the tunnel is already fastest.
//
//   - It is not always available. A path that refuses it fails the write, so
//     shipping it means a fallback and a way to decide, on the data path.
//
//     go test ./internal/tunnel/l3/ -run TestGSOSendRate -v -count=1
func TestGSOSendRate(t *testing.T) {
	if testing.Short() {
		t.Skip("a measurement, not a check")
	}

	sink, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding a sink: %v", err)
	}
	defer sink.Close()
	// Drained, or this measures how fast the kernel drops things.
	go func() {
		b := make([]byte, 65536)
		for {
			if _, _, err := sink.ReadFrom(b); err != nil {
				return
			}
		}
	}()
	to := sink.LocalAddr()

	const (
		datagrams = 40000
		segment   = 1200
		// segments is what one GSO write carries. The kernel allows up to 64,
		// but the whole run is one UDP payload as far as the send call is
		// concerned and a UDP payload stops at 65,507 bytes — 64 × 1200 is over
		// that and the write is refused outright. 48 fits with room to spare,
		// and is already six times the batch that ships.
		segments = 48
	)
	payload := make([]byte, segment)

	// sendmmsg, the path that ships today.
	batched := func() (time.Duration, int) {
		c := newLocalUDPCarrier(t)
		bufs := make([][]byte, batchSize)
		for i := range bufs {
			bufs[i] = payload
		}
		sent, syscalls := 0, 0
		start := time.Now()
		for sent < datagrams {
			n := batchSize
			if rest := datagrams - sent; rest < n {
				n = rest
			}
			got, err := c.WriteBatch(bufs[:n], to)
			if err != nil {
				t.Fatalf("WriteBatch: %v", err)
			}
			sent += got
			syscalls++
		}
		return time.Since(start), syscalls
	}

	// gsoN is the same write with a chosen run length.
	gsoN := func(n int) (time.Duration, int, error) { return gsoRun(t, to, datagrams, segment, n) }

	// One sendmsg per run of segments, cut up by the kernel.
	//
	// The socket is bound and not connected, and the destination travels with
	// each write — the same shape the carrier has, because an l3 tunnel's peer
	// address is learned and can move. A connected socket would measure faster
	// and would not be measuring the thing that could ship.
	gso := func() (time.Duration, int, error) {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			return 0, 0, err
		}
		defer conn.Close()
		raw, err := conn.SyscallConn()
		if err != nil {
			return 0, 0, err
		}

		big := make([]byte, segment*segments)
		for i := range big {
			big[i] = byte(i)
		}
		// UDP_SEGMENT as ancillary data, so the size travels with the write
		// rather than being a property of the socket.
		cmsg := make([]byte, unix.CmsgSpace(2))
		h := (*unix.Cmsghdr)(unsafe.Pointer(&cmsg[0]))
		h.Level = unix.IPPROTO_UDP
		h.Type = unix.UDP_SEGMENT
		h.SetLen(unix.CmsgLen(2))
		binary.NativeEndian.PutUint16(cmsg[unix.CmsgLen(0):], segment)

		dst := to.(*net.UDPAddr)
		sa := &unix.SockaddrInet4{Port: dst.Port}
		copy(sa.Addr[:], dst.IP.To4())

		var opErr error
		sent, syscalls := 0, 0
		start := time.Now()
		if err := raw.Control(func(fd uintptr) {
			for sent < datagrams {
				n := segments
				if rest := datagrams - sent; rest < n {
					n = rest
				}
				if _, err := unix.SendmsgN(int(fd), big[:segment*n], cmsg, sa, 0); err != nil {
					opErr = err
					return
				}
				sent += n
				syscalls++
			}
		}); err != nil {
			return 0, 0, err
		}
		return time.Since(start), syscalls, opErr
	}

	bd, bs := batched()
	// At the batch the carrier actually has today, so the comparison is not
	// secretly a comparison of batch sizes. This is the number that decides
	// whether GSO is worth building: if the gain lives in the batch rather than
	// in the offload, the answer is to read more from the TUN, not to segment.
	sd, ss, serr := gsoN(batchSize)
	gd, gs, gerr := gso()
	if gerr != nil {
		t.Skipf("this kernel or socket refuses UDP_SEGMENT (%v), which is itself "+
			"the answer: shipping it would need a fallback on the data path", gerr)
	}

	rate := func(d time.Duration) float64 { return float64(datagrams) / d.Seconds() / 1000 }
	t.Logf("sendmmsg    (%2d per call): %6d syscalls  %7.0f kpps", batchSize, bs, rate(bd))
	if serr == nil {
		t.Logf("UDP_SEGMENT (%2d per call): %6d syscalls  %7.0f kpps  %+.1f%% vs sendmmsg at the same batch",
			batchSize, ss, rate(sd), 100*(rate(sd)/rate(bd)-1))
	}
	t.Logf("UDP_SEGMENT (%2d per call): %6d syscalls  %7.0f kpps  %+.1f%% vs sendmmsg",
		segments, gs, rate(gd), 100*(rate(gd)/rate(bd)-1))
}

// gsoRun sends total datagrams of segment bytes, perCall at a time, through one
// sendmsg carrying UDP_SEGMENT. It reports how long that took and how many
// syscalls it was.
func gsoRun(t *testing.T, to net.Addr, total, segment, perCall int) (time.Duration, int, error) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return 0, 0, err
	}
	defer conn.Close()
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, err
	}

	big := make([]byte, segment*perCall)
	cmsg := make([]byte, unix.CmsgSpace(2))
	h := (*unix.Cmsghdr)(unsafe.Pointer(&cmsg[0]))
	h.Level = unix.IPPROTO_UDP
	h.Type = unix.UDP_SEGMENT
	h.SetLen(unix.CmsgLen(2))
	binary.NativeEndian.PutUint16(cmsg[unix.CmsgLen(0):], uint16(segment))

	dst := to.(*net.UDPAddr)
	sa := &unix.SockaddrInet4{Port: dst.Port}
	copy(sa.Addr[:], dst.IP.To4())

	var opErr error
	sent, syscalls := 0, 0
	start := time.Now()
	if err := raw.Control(func(fd uintptr) {
		for sent < total {
			n := perCall
			if rest := total - sent; rest < n {
				n = rest
			}
			if _, err := unix.SendmsgN(int(fd), big[:segment*n], cmsg, sa, 0); err != nil {
				opErr = err
				return
			}
			sent += n
			syscalls++
		}
	}); err != nil {
		return 0, 0, err
	}
	return time.Since(start), syscalls, opErr
}
