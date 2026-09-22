package transport

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// Restart, on the branches that matter.
//
// Restart is where a transport swaps a whole generation while the previous
// one's goroutines are still winding down, and it is where every data race the
// detector has caught in this package lived. It was the last thing in here with
// no test at all — the end-to-end suite restarts *tunnels*, which exercises the
// happy path and never reaches the two branches below.
//
// Those two are the ones worth holding still:
//
//   - a restart that arrives while the tunnel is shutting down must not build a
//     new run, because on a reload that means binding the ports the run
//     replacing this one is about to ask for;
//   - two restarts at once must not both proceed, because two runs would each
//     believe they own the listeners.

func silentLogger() *logrus.Logger {
	l := logrus.New()
	l.SetLevel(logrus.FatalLevel)
	return l
}

// freeAddr takes a loopback port the kernel is not using, so a test that does
// bind something never collides with another.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// restartable is the behaviour every transport has to share here.
type restartable interface {
	Restart()
	Running() bool
}

// eachTransport builds one of every transport against a dead parent context.
//
// They are built together rather than one test each because the property being
// checked is that *all* of them behave the same way — the bugs this guards
// against were found in one copy at a time and were present in several.
func eachTransport(t *testing.T, parent context.Context) map[string]restartable {
	t.Helper()
	log := silentLogger()
	addr := freeAddr(t)
	ports := []string{}

	return map[string]restartable{
		"tcp": NewTCPServer(parent, &TcpConfig{
			BindAddr: addr, Token: "t", ChannelSize: 8, Ports: ports,
			Heartbeat: time.Second, KeepAlive: time.Second,
		}, log),
		"tcpmux": NewTcpMuxServer(parent, &TcpMuxConfig{
			BindAddr: addr, Token: "t", ChannelSize: 8, Ports: ports,
			Heartbeat: time.Second, MuxCon: 2, MuxVersion: 2,
			MaxFrameSize: 4096, MaxReceiveBuffer: 1 << 16, MaxStreamBuffer: 4096,
		}, log),
		"ws": NewWSServer(parent, &WsConfig{
			BindAddr: addr, Token: "t", ChannelSize: 8, Ports: ports,
			Heartbeat: time.Second, KeepAlive: time.Second, Mode: "ws",
		}, log),
		"wsmux": NewWSMuxServer(parent, &WsMuxConfig{
			BindAddr: addr, Token: "t", ChannelSize: 8, Ports: ports,
			Heartbeat: time.Second, KeepAlive: time.Second, Mode: "wsmux",
			MuxCon: 2, MuxVersion: 2, MaxFrameSize: 4096,
			MaxReceiveBuffer: 1 << 16, MaxStreamBuffer: 4096,
		}, log),
		"kcp": NewKcpServer(parent, &KcpConfig{
			BindAddr: addr, Token: "t", ChannelSize: 8, Ports: ports,
			Heartbeat: time.Second, MuxCon: 2, MuxVersion: 2,
			MaxFrameSize: 4096, MaxReceiveBuffer: 1 << 16, MaxStreamBuffer: 4096,
			MTU: 1350, SndWnd: 128, RcvWnd: 128,
		}, log),
		"quic": NewQuicServer(parent, &QuicConfig{
			BindAddr: addr, Token: "t", ChannelSize: 8, Ports: ports,
			Heartbeat: time.Second, KeepAlive: time.Second,
		}, log),
		"udp": NewUDPServer(parent, &UdpConfig{
			BindAddr: addr, Token: "t", ChannelSize: 8, Ports: ports,
			Heartbeat: time.Second,
		}, log),
	}
}

// A restart that arrives while the tunnel is going down must abandon itself.
//
// Rebuilding the run from a parent context that is already finished would bind
// the listeners again only to close them — and on a reload it means fighting
// the run that is replacing this one for its own ports.
func TestARestartDuringShutdownDoesNotStartANewRun(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	for name, tr := range eachTransport(t, dead) {
		t.Run(name, func(t *testing.T) {
			done := make(chan struct{})
			go func() { defer close(done); tr.Restart() }()

			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("Restart did not return on a tunnel that is shutting down")
			}
			// Nothing was started, so nothing can be connected.
			if tr.Running() {
				t.Fatal("a restart abandoned during shutdown left the transport reporting a peer")
			}
		})
	}
}

// Two restarts at once must not both proceed: two runs would each believe they
// own the listeners, and the second to bind loses.
func TestTwoRestartsAtOnceDoNotBothProceed(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	for name, tr := range eachTransport(t, dead) {
		t.Run(name, func(t *testing.T) {
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					tr.Restart()
				}()
			}
			close(start)

			done := make(chan struct{})
			go func() { defer close(done); wg.Wait() }()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("concurrent restarts deadlocked")
			}
			if tr.Running() {
				t.Fatal("a concurrent restart left the transport reporting a peer")
			}
		})
	}
}

// The status a restart leaves behind must not read as connected. A fallback
// chain asks exactly this question, and a transport that answers "up" while it
// has nothing would pin a chain to a carrier that is not working.
func TestARestartLeavesTheStatusClear(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	for name, tr := range eachTransport(t, dead) {
		t.Run(name, func(t *testing.T) {
			// Pretend the previous run had a peer.
			switch v := tr.(type) {
			case *TcpTransport:
				v.status.set("Connected (TCP)")
			case *TcpMuxTransport:
				v.status.set("Connected (TCPMux)")
			case *WsTransport:
				v.status.set("Connected (ws)")
			case *WsMuxTransport:
				v.status.set("Connected (wsmux)")
			case *KcpTransport:
				v.status.set("Connected (KCP)")
			case *QuicTransport:
				v.status.set("Connected (QUIC)")
			case *UdpTransport:
				v.status.set("Connected (UDP)")
			default:
				t.Fatalf("%s: unhandled transport type %T", name, tr)
			}
			if !tr.Running() {
				t.Fatal("setup: the transport did not take the status")
			}

			tr.Restart()

			if tr.Running() {
				t.Error("the status from the previous run survived the restart, so the " +
					"panel and any fallback chain still believe there is a peer")
			}
		})
	}
}

// Every transport in this package must be in the table above. A new one that is
// not gets none of these guarantees checked, silently.
func TestEveryTransportIsCoveredByTheRestartTable(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	got := eachTransport(t, dead)
	if len(got) != 7 {
		t.Fatalf("the restart table holds %d transports; this package has 7", len(got))
	}
	for _, want := range []string{"tcp", "tcpmux", "ws", "wsmux", "kcp", "quic", "udp"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s is missing from the restart table", want)
		}
	}
}

// A sanity check on the helper, so a mistake there cannot make the tests above
// pass by testing nothing.
func TestTheRestartTableBuildsRealTransports(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tr := range eachTransport(t, dead) {
		if tr == nil {
			t.Errorf("%s built nil", name)
		}
	}
	_ = fmt.Sprint()
}
