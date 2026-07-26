package socks

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// A UDP datagram sent through UDP ASSOCIATE must reach the destination and its
// reply must come back — proving the built-in SOCKS5 proxy carries UDP.
func TestUDPAssociateRoundTrip(t *testing.T) {
	// UDP echo destination.
	echo, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		b := make([]byte, 65535)
		for {
			n, addr, err := echo.ReadFromUDP(b)
			if err != nil {
				return
			}
			echo.WriteToUDP(b[:n], addr)
		}
	}()
	echoAddr := echo.LocalAddr().(*net.UDPAddr)

	// SOCKS5 proxy, no-auth.
	pl, _ := net.Listen("tcp", "127.0.0.1:0")
	proxyAddr := pl.Addr().String()
	pl.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Serve(ctx, proxyAddr, nil)
	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", proxyAddr, 100*time.Millisecond); err == nil {
			c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Control connection: greet no-auth, then UDP ASSOCIATE.
	tc, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	tc.Write([]byte{ver5, 0x01, authNone})
	greet := make([]byte, 2)
	if _, err := io.ReadFull(tc, greet); err != nil || greet[1] != authNone {
		t.Fatalf("no-auth greeting failed: %v %x", err, greet)
	}
	// VER CMD RSV ATYP DST.ADDR(0.0.0.0) DST.PORT(0)
	tc.Write([]byte{ver5, cmdUDP, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0})

	rhead := make([]byte, 4)
	if _, err := io.ReadFull(tc, rhead); err != nil || rhead[1] != repOK {
		t.Fatalf("UDP ASSOCIATE reply failed: %v %x", err, rhead)
	}
	bnd := make([]byte, 4+2) // IPv4 + port
	if _, err := io.ReadFull(tc, bnd); err != nil {
		t.Fatal(err)
	}
	relay := &net.UDPAddr{IP: net.IP(bnd[:4]), Port: int(binary.BigEndian.Uint16(bnd[4:6]))}
	if relay.IP.IsUnspecified() {
		relay.IP = net.IPv4(127, 0, 0, 1)
	}

	// Send a wrapped datagram to the relay naming the echo destination.
	uc, err := net.DialUDP("udp", nil, relay)
	if err != nil {
		t.Fatal(err)
	}
	defer uc.Close()

	dg := []byte{0x00, 0x00, 0x00, atypIPv4}
	dg = append(dg, echoAddr.IP.To4()...)
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(echoAddr.Port))
	dg = append(dg, p[:]...)
	payload := []byte("udp-associate-hello")
	dg = append(dg, payload...)
	if _, err := uc.Write(dg); err != nil {
		t.Fatal(err)
	}

	uc.SetReadDeadline(time.Now().Add(3 * time.Second))
	rbuf := make([]byte, 65535)
	n, err := uc.Read(rbuf)
	if err != nil {
		t.Fatalf("no reply through the UDP relay: %v", err)
	}
	// Reply = header (RSV2+FRAG1+ATYP1+IPv4(4)+PORT(2) = 10) + echoed DATA.
	const hdr = 3 + 1 + 4 + 2
	if n <= hdr {
		t.Fatalf("reply too short: %d bytes", n)
	}
	if got := string(rbuf[hdr:n]); got != string(payload) {
		t.Fatalf("echoed data = %q, want %q", got, payload)
	}
}
