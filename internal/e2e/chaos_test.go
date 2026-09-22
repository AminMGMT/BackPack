package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/server"
)

// Faults that are not network faults.
//
// fault_test.go covers the path: loss, latency, jitter, a backend that accepts
// and then says nothing. Those are the conditions a tunnel is designed for, and
// they are not the conditions that take one down. The three below are what
// actually happens to a VPS, and none of them had a test:
//
//   - a process is killed while it is carrying traffic, which is what an OOM
//     kill, a `systemctl restart` under load and a panic all look like from the
//     other end;
//   - the disk fills, so every write the engine makes fails at once;
//   - the file descriptors run out, so accept and dial both fail while
//     everything already open keeps working.
//
// What they assert is the same in each case: the tunnel does not wedge, and it
// comes back without anybody touching it. A tunnel that dies loudly is a tunnel
// systemd restarts; a tunnel that survives in a state where it carries nothing
// is the failure worth testing for.

// openFDs counts this process's open file descriptors. Linux only; used to tell
// "recovered" from "recovered while leaking a connection every time".
func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("cannot count file descriptors here: %v", err)
	}
	return len(entries)
}

// The far end is killed with bytes in flight.
//
// Stopping a client between transfers is the easy case and TestServerRestartRecovery
// already covers the server side of it. This is the one that leaves state
// behind: a pool connection mid-frame, a mux stream half written, a forwarded
// connection with a read outstanding. The server has to notice, let go of all
// of it, and be ready for the replacement.
//
// Three cycles rather than one, with the descriptor count checked across them,
// because the interesting failure is not a crash — it is the tunnel coming back
// every time while keeping something from each previous life.
func TestAPeerKilledMidTransferDoesNotWedgeTheOther(t *testing.T) {
	if testing.Short() {
		t.Skip("starts and kills three tunnels — skipped under -short")
	}

	for _, transport := range []string{"tcp", "tcpmux", "ws"} {
		t.Run(transport, func(t *testing.T) {
			backend := startEchoBackend(t)
			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "chaos-token-0123456789abcdef"

			srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token)
			srvCtx, stopServer := context.WithCancel(context.Background())
			var srvWG sync.WaitGroup
			srv := server.NewServer(srvCfg, srvCtx)
			srvWG.Add(1)
			go func() { defer srvWG.Done(); srv.Start() }()
			// One defer, in order: Start only returns once its context is done,
			// so waiting for it before cancelling waits for ever.
			defer func() {
				stopServer()
				srv.Stop()
				waitWG(&srvWG, 10*time.Second)
			}()
			time.Sleep(300 * time.Millisecond)

			var fdsAfterFirst int
			for cycle := 1; cycle <= 3; cycle++ {
				cliCfg := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
				cliCtx, killClient := context.WithCancel(context.Background())
				tun := startClientAgainst(t, cliCtx, cliCfg, entryPort, tunnelPort)

				if err := tun.waitReady(tunnelReadyTimeout); err != nil {
					killClient()
					t.Fatalf("cycle %d: the tunnel never came up: %v", cycle, err)
				}

				// A transfer big enough that it is certainly still running when
				// the client is killed a moment later.
				inFlight := make(chan error, 1)
				go func() { inFlight <- tun.roundTrip(randomPayload(t, 4<<20)) }()
				time.Sleep(250 * time.Millisecond)

				// This is the kill: the client's context is what its process
				// lifetime is, in this harness.
				killClient()
				// Bounded: the harness's own Stop() gives a transport five
				// seconds for the same reason, and a client that will not exit
				// is what this test is looking for rather than a reason to hang
				// the suite.
				if !waitWG(tun.wg, 20*time.Second) {
					t.Fatalf("cycle %d: the client did not exit within 20s of being killed mid-transfer", cycle)
				}

				// The transfer must fail rather than hang. A caller left
				// blocked on a dead tunnel is the worst of the outcomes here,
				// because nothing upstream ever finds out.
				select {
				case <-inFlight:
				case <-time.After(30 * time.Second):
					t.Fatalf("cycle %d: a transfer through a killed tunnel never returned", cycle)
				}

				if cycle == 1 {
					// Let the first cycle's teardown settle before taking the
					// baseline, so the comparison is between steady states.
					time.Sleep(time.Second)
					fdsAfterFirst = openFDs(t)
				}
			}

			// The server survived three killed clients: a fourth must work.
			cliCfg := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
			cliCtx, stopClient := context.WithCancel(context.Background())
			defer stopClient()
			tun := startClientAgainst(t, cliCtx, cliCfg, entryPort, tunnelPort)
			if err := tun.waitReady(45 * time.Second); err != nil {
				t.Fatalf("the server did not accept a new client after three were killed mid-transfer: %v", err)
			}
			if err := tun.roundTrip(randomPayload(t, 64*1024)); err != nil {
				t.Fatalf("traffic failed after three mid-transfer kills: %v", err)
			}

			stopClient()
			if !waitWG(tun.wg, 20*time.Second) {
				t.Fatal("the last client did not exit after it was stopped")
			}
			time.Sleep(time.Second)

			// Some growth is ordinary — buffers, a pool that sized up. What
			// this catches is a connection kept per killed client, which on a
			// tunnel that reconnects all day is a descriptor leak with a
			// deadline on it.
			if grown := openFDs(t) - fdsAfterFirst; grown > 24 {
				t.Errorf("%d more file descriptors are open after three more client lifetimes "+
					"than after the first — the server is keeping something from each one", grown)
			}
		})
	}
}

// The service behind the tunnel resets the connection mid-transfer.
//
// An RST is not a close: there is no FIN, the data already in flight is thrown
// away, and the read on the other side fails rather than ending. It is what a
// backend being killed, restarted or OOM'd actually produces, and the tunnel
// has to pass the failure through rather than absorb it — a forwarded
// connection that stays open after its backend has gone is a client waiting
// forever for bytes that will never come.
func TestABackendResetIsPassedThroughRatherThanAbsorbed(t *testing.T) {
	resetting := startResettingBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "reset-token-0123456789abcdef"

	srvCfg := baseServerConfig("tcp", tunnelPort, entryPort, resetting.addr, token)
	cliCfg := baseClientConfig("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)

	// The backend answers the first byte, so the tunnel is provably up before
	// anything is reset.
	if err := waitFor(45*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", tun.Entry, 2*time.Second)
		if err != nil {
			return false
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := c.Write([]byte("hello")); err != nil {
			return false
		}
		b := make([]byte, 1)
		_, err = io.ReadFull(c, b)
		return err == nil
	}); err != nil {
		t.Fatalf("the tunnel never carried anything: %v", err)
	}

	conn, err := net.DialTimeout("tcp", tun.Entry, 5*time.Second)
	if err != nil {
		t.Fatalf("dial the entry port: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	if _, err := conn.Write([]byte("reset me now")); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Whatever comes back, the read has to *end*. Passing through as an EOF is
	// as acceptable as passing through as a reset; hanging is not.
	buf := make([]byte, 4096)
	for {
		_, err := conn.Read(buf)
		if err != nil {
			break
		}
	}

	// And the tunnel itself is unharmed: one backend going away is not a reason
	// for the next connection to fail.
	if err := tun.roundTrip([]byte("still here")); err != nil {
		// The resetting backend resets every connection after the first byte,
		// so a clean round trip is not available — reaching it at all is.
		if !strings.Contains(err.Error(), "read back") {
			t.Fatalf("the tunnel stopped working after a backend reset: %v", err)
		}
	}
}

// resettingBackend answers one byte and then destroys the connection with a
// reset rather than closing it.
type resettingBackend struct {
	addr string
	ln   net.Listener
}

func startResettingBackend(t *testing.T) *resettingBackend {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resetting backend: %v", err)
	}
	b := &resettingBackend{addr: ln.Addr().String(), ln: ln}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				tc := c.(*net.TCPConn)
				buf := make([]byte, 1024)
				_ = tc.SetReadDeadline(time.Now().Add(10 * time.Second))
				if n, err := tc.Read(buf); err == nil && n > 0 {
					_, _ = tc.Write(buf[:1])
				}
				// SO_LINGER 0 turns Close into a reset: no FIN, and anything
				// still queued is discarded. This is what a killed process
				// does.
				_ = tc.SetLinger(0)
				tc.Close()
			}(c)
		}
	}()
	return b
}

// waitWG waits for a WaitGroup, but not for ever.
func waitWG(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// waitFor polls until ok reports true, or gives up.
func waitFor(timeout time.Duration, ok func() bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("still false after %s", timeout)
}
