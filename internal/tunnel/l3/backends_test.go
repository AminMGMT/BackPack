package l3

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

// A new connection goes to the member carrying the fewest, not the next in
// turn: one long download must not keep drawing its share of new work.
func TestTheLeastLoadedMemberGetsTheNextConnection(t *testing.T) {
	a := echoTCP(t, "A:")
	b := echoTCP(t, "B:")
	pool := newBackendPool([]string{a, b})

	// Three connections held open on whichever member they land on.
	var held []net.Conn
	for i := 0; i < 3; i++ {
		c, _, err := pool.dial(context.Background(), "tcp")
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()
	ma, mb := pool.members[0].active.Load(), pool.members[1].active.Load()
	if d := ma - mb; d > 1 || d < -1 {
		t.Fatalf("three connections split %d/%d, want as even as possible", ma, mb)
	}

	// Load one member heavily; every new connection must go to the other.
	pool.members[0].active.Add(10)
	for i := 0; i < 5; i++ {
		c, m, err := pool.dial(context.Background(), "tcp")
		if err != nil {
			t.Fatal(err)
		}
		if m != pool.members[1] {
			t.Fatalf("connection %d went to the loaded member", i)
		}
		c.Close()
		m.done()
	}
}

// A member whose dial failed is passed over until its cooldown ends, so the
// connections after the first failure do not each wait on it again.
func TestAFailedMemberIsSetAsideThenRetried(t *testing.T) {
	live := echoTCP(t, "L:")
	dead := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	clock := time.Now()
	pool := newBackendPool([]string{dead, live})
	pool.now = func() time.Time { return clock }

	// Make the dead member the first choice, then fail it once.
	pool.members[1].active.Add(5)
	if _, m, err := pool.dial(context.Background(), "tcp"); err != nil || m.addr != live {
		t.Fatalf("dial = %v, %v; want the live member", m, err)
	}
	if pool.members[0].downUntil.Load() == 0 {
		t.Fatal("the failed member was not set aside")
	}
	// While it cools down it goes to the back, loaded or not.
	if first := pool.order()[0]; first.addr != live {
		t.Fatalf("a cooling member was tried first: %s", first.addr)
	}
	// After the cooldown it is back in the running.
	clock = clock.Add(backendCooldown + time.Second)
	if first := pool.order()[0]; first.addr != dead {
		t.Fatalf("after its cooldown the member was not tried again first: %s", first.addr)
	}
}

// With every member cooling down the connection is still attempted rather
// than refused: a member marked down may have come back.
func TestAllMembersCoolingStillDials(t *testing.T) {
	live := echoTCP(t, "L:")
	pool := newBackendPool([]string{live})
	pool.members[0].downUntil.Store(time.Now().Add(time.Hour).UnixNano())
	c, m, err := pool.dial(context.Background(), "tcp")
	if err != nil {
		t.Fatalf("a cooling but live member was refused: %v", err)
	}
	c.Close()
	m.done()
	if pool.members[0].downUntil.Load() != 0 {
		t.Fatal("a member that answered is still marked down")
	}
}

// A member that hangs rather than refusing costs a connection at most
// memberDialTimeout while another member is left to try.
func TestAHangingMemberIsAbandonedQuickly(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out a dial timeout")
	}
	live := echoTCP(t, "L:")
	// A non-routable address: the SYN goes nowhere, the dial hangs.
	hang := "10.255.255.1:9"
	pool := newBackendPool([]string{hang, live})
	pool.members[1].active.Add(5) // make the hanging one the first choice
	began := time.Now()
	c, m, err := pool.dial(context.Background(), "tcp")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.Close()
	m.done()
	if took := time.Since(began); took > memberDialTimeout+2*time.Second {
		t.Fatalf("took %s to fall through a hanging member", took)
	}
}

// A burst of connections dialled at once spreads evenly: each dial counts
// against its member before it completes, so the next one in the burst sees
// it.
func TestABurstSpreadsEvenly(t *testing.T) {
	a := echoTCP(t, "A:")
	b := echoTCP(t, "B:")
	pool := newBackendPool([]string{a, b})

	const burst = 16
	conns := make(chan net.Conn, burst)
	errs := make(chan error, burst)
	for i := 0; i < burst; i++ {
		go func() {
			c, _, err := pool.dial(context.Background(), "tcp")
			if err != nil {
				errs <- err
				return
			}
			conns <- c
		}()
	}
	for i := 0; i < burst; i++ {
		select {
		case c := <-conns:
			defer c.Close()
		case err := <-errs:
			t.Fatal(err)
		}
	}
	ma, mb := pool.members[0].active.Load(), pool.members[1].active.Load()
	if ma+mb != burst || ma < burst/2-2 || mb < burst/2-2 {
		t.Fatalf("a burst of %d split %d/%d", burst, ma, mb)
	}
}
