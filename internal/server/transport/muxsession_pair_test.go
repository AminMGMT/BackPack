package transport

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/utils"
	"github.com/xtaci/smux"
)

// slowWrites makes every write to the carrier take d, the way a session whose
// window is full makes a frame wait for the ones ahead of it.
type slowWrites struct {
	net.Conn
	d time.Duration
}

func (s slowWrites) Write(p []byte) (int, error) {
	time.Sleep(s.d)
	return s.Conn.Write(p)
}

// A session pairs the connections waiting for it without waiting for each
// one's backend announcement to get through the write queue before it takes the
// next: the announcement is queued behind the other streams' frames, and doing
// them one at a time capped the pairing rate at one per wait, so a flood of
// short connections made every new connection queue until the pairing timeout.
func TestASessionPairsWaitingConnectionsConcurrently(t *testing.T) {
	const (
		conns = 8
		bulk  = 6
		d     = 10 * time.Millisecond
	)
	a, b := net.Pipe()
	cfg := smux.DefaultConfig()
	cfg.Version = 2
	cfg.KeepAliveDisabled = true
	cfg.MaxFrameSize = 4096
	cli, err := smux.Client(slowWrites{a, d}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := smux.Server(b, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// The other end: streams that carry a name are paired ones, the rest are
	// bulk and are drained.
	var paired atomic.Int32
	allPaired := make(chan struct{})
	go func() {
		for {
			st, err := srv.AcceptStream()
			if err != nil {
				return
			}
			go func() {
				_ = st.SetReadDeadline(time.Now().Add(5 * time.Second))
				name, err := utils.ReceiveBinaryString(st)
				if err != nil {
					return
				}
				if name == "bulk" {
					_ = st.SetReadDeadline(time.Time{})
					io.Copy(io.Discard, st)
					return
				}
				if int(paired.Add(1)) == conns {
					close(allPaired)
				}
			}()
		}
	}()

	// Bulk streams keep the write queue busy for the whole test.
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() {
		cancel()
		cli.Close()
		srv.Close()
		wg.Wait()
	}()
	for i := 0; i < bulk; i++ {
		st, err := cli.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		if err := utils.SendBinaryString(st, "bulk"); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 4096)
			for ctx.Err() == nil {
				if _, err := st.Write(buf); err != nil {
					return
				}
			}
		}()
	}

	local := make(chan LocalTCPConn, conns)
	var keep []net.Conn
	for i := 0; i < conns; i++ {
		c1, c2 := net.Pipe()
		keep = append(keep, c1, c2)
		local <- LocalTCPConn{conn: c1, remoteAddr: "backend", timeCreated: nowMillis()}
	}
	defer func() {
		for _, c := range keep {
			c.Close()
		}
	}()

	var streams, sessions int32
	m := muxSession{
		ctx: ctx, local: local, muxCon: conns, log: quietLogger(),
		streams: &streams, sessions: &sessions,
	}
	go m.run(cli)

	start := time.Now()
	select {
	case <-allPaired:
	case <-time.After(10 * time.Second):
		t.Fatalf("only %d of %d connections were paired", paired.Load(), conns)
	}
	took := time.Since(start)
	t.Logf("paired %d connections in %v", conns, took)
	// One at a time, each connection costs its own write and a turn behind the
	// bulk streams' frames: conns x (2 + bulk) writes, 640 ms here. Together
	// the announcements share the turns, about two thirds of that.
	if limit := time.Duration(conns*(2+bulk)) * d * 85 / 100; took > limit {
		t.Fatalf("pairing %d connections took %v, over %v: announcements are being sent one at a time", conns, took, limit)
	}
}
