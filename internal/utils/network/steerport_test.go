package network

import (
	"net"
	"testing"
	"time"
)

const defaultProbeTimeoutForTest = time.Second

// Under health steering, the address the control channel's race reached is
// where the pool goes. Reported as a reverse tunnel whose control channel came
// up on a backup address while every data connection kept dialling the
// primary, which answered ping and had its tunnel port closed.
func TestTheRacesWinnerSteersThePool(t *testing.T) {
	e := NewEndpoints("primary:443", "backup:443")
	e.preferred.Store(0)
	e.steer.Store(true)
	e.Prefer("backup:443")
	if e.Next() != "backup:443" || e.Current() != "backup:443" {
		t.Fatalf("the pool dials %q after the race was won by the backup", e.Next())
	}
}

// A TCP tunnel port that refuses is not reachable, whatever ping says.
func TestTheReachProbeAsksTheTunnelPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	open := l.Addr().String()
	closedL, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := closedL.Addr().String()
	closedL.Close()
	defer l.Close()

	reach := TCPReach(defaultProbeTimeoutForTest)
	if !reach(open) {
		t.Error("a listening port was reported unreachable")
	}
	if reach(closed) {
		t.Error("a closed port was reported reachable")
	}

	e := NewEndpoints("a", "b")
	e.SetReachProbe(reach)
	if e.reach.Load() == nil {
		t.Fatal("the probe was not kept")
	}
	e.SetReachProbe(nil)
	if e.reach.Load() != nil {
		t.Fatal("the probe was not cleared for a datagram transport")
	}
}
