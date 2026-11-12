package paqet

import (
	"context"
	"io"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// A fake wire: two ends joined in memory, with optional loss, standing where
// libpcap stands. Everything above the Capture and Injector seam runs for real.

type fakeFrame struct {
	payload []byte
	src     *net.UDPAddr
	dport   uint16
}

type fakeEnd struct {
	ip     net.IP
	in     chan fakeFrame
	peer   *fakeEnd
	lossFn func() bool // true drops
	mu     sync.Mutex
	closed chan struct{}
	once   sync.Once
	sent   atomic.Int64
}

func newFakePair() (a, b *fakeEnd) {
	mk := func(ip string) *fakeEnd {
		return &fakeEnd{ip: net.ParseIP(ip).To4(), in: make(chan fakeFrame, 4096), closed: make(chan struct{})}
	}
	a, b = mk("10.0.0.1"), mk("10.0.0.2")
	a.peer, b.peer = b, a
	return
}

func (f *fakeEnd) closedCh() chan struct{} { f.mu.Lock(); defer f.mu.Unlock(); return f.closed }

// reopen makes a closed end usable again, as a restarted process's wire is.
func (f *fakeEnd) reopen() {
	f.mu.Lock()
	f.closed = make(chan struct{})
	f.once = sync.Once{}
	f.mu.Unlock()
}

func (f *fakeEnd) Next() ([]byte, *net.UDPAddr, uint16, net.HardwareAddr, error) {
	closed := f.closedCh()
	select {
	case fr := <-f.in:
		return fr.payload, fr.src, fr.dport, nil, nil
	case <-closed:
		return nil, nil, 0, nil, net.ErrClosed
	case <-time.After(50 * time.Millisecond):
		return nil, nil, 0, nil, errNoFrame
	}
}

func (f *fakeEnd) Inject(srcPort uint16, payload []byte, dst *net.UDPAddr) error {
	f.sent.Add(1)
	f.mu.Lock()
	drop := f.lossFn != nil && f.lossFn()
	f.mu.Unlock()
	if drop {
		return nil
	}
	cp := append([]byte(nil), payload...)
	select {
	case f.peer.in <- fakeFrame{payload: cp, src: &net.UDPAddr{IP: f.ip, Port: int(srcPort)}, dport: uint16(dst.Port)}:
	default:
	}
	return nil
}

func (f *fakeEnd) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.once.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeEnd) setLoss(p float64) {
	r := rand.New(rand.NewSource(7))
	var mu sync.Mutex
	f.mu.Lock()
	f.lossFn = func() bool { mu.Lock(); defer mu.Unlock(); return r.Float64() < p }
	f.mu.Unlock()
}

// fakeLease hands out source ports from 40000 and counts claims.
type fakeLease struct {
	mu      sync.Mutex
	next    uint16
	claimed map[uint16]bool
	claims  []uint16 // every port handed out, in order
	guard   bool
}

func newFakeLease() *fakeLease {
	return &fakeLease{next: 40000, claimed: map[uint16]bool{}, guard: true}
}
func (l *fakeLease) ClaimPort() (uint16, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for p := uint16(40000); p < 40128; p++ {
		if !l.claimed[p] {
			l.claimed[p] = true
			l.claims = append(l.claims, p)
			return p, nil
		}
	}
	return 0, io.ErrShortBuffer
}
func (l *fakeLease) ReleasePort(p uint16) { l.mu.Lock(); delete(l.claimed, p); l.mu.Unlock() }
func (l *fakeLease) GuardInstalled() bool { return l.guard }
func (l *fakeLease) Close()               { l.mu.Lock(); clear(l.claimed); l.mu.Unlock() } // as PckLease.Close
func (l *fakeLease) inUse() int           { l.mu.Lock(); defer l.mu.Unlock(); return len(l.claimed) }
func (l *fakeLease) history() []uint16 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]uint16(nil), l.claims...)
}

const testPort = 9443

// pair is a client engine and a server engine over a fake wire.
type pair struct {
	client, server *Engine
	cEnd, sEnd     *fakeEnd
	cLease         *fakeLease
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	cerr, serr     chan error
}

func testLog() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

func newPair(t *testing.T, tune func(client, server *Config)) *pair {
	t.Helper()
	p := &pair{cerr: make(chan error, 8), serr: make(chan error, 8)}
	p.cEnd, p.sEnd = newFakePair()
	p.cLease = newFakeLease()
	pp, _ := PresetParams(PresetBalance)
	pp.Mode = ModeFast3 // quick retransmits keep the tests short
	pp.KeepAlive, pp.KeepAliveTimeout = 300*time.Millisecond, 2*time.Second
	base := Config{Token: "a-long-test-token", Params: pp}
	cc, sc := base, base
	cc.Addr = "10.0.0.2:9443"
	sc.Server = true
	sc.Addr = "0.0.0.0:9443"
	if tune != nil {
		tune(&cc, &sc)
	}
	var err error
	if p.client, err = New(cc, testLog()); err != nil {
		t.Fatal(err)
	}
	if p.server, err = New(sc, testLog()); err != nil {
		t.Fatal(err)
	}
	p.client.open = func(context.Context) (*wireSet, error) {
		w := NewWire(p.cEnd, p.cEnd, p.cEnd.ip)
		return &wireSet{wire: w, lease: p.cLease, local: p.cEnd.ip, close: func() { w.Close(); p.cLease.Close() }}, nil
	}
	p.server.open = func(context.Context) (*wireSet, error) {
		w := NewWire(p.sEnd, p.sEnd, p.sEnd.ip)
		s, err := w.Open(testPort, nil)
		if err != nil {
			return nil, err
		}
		return &wireSet{wire: w, lease: newFakeLease(), local: p.sEnd.ip, serverSocket: s, listenPort: testPort,
			close: func() { w.Close() }}, nil
	}
	return p
}

func (p *pair) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.wg.Add(2)
	go func() { defer p.wg.Done(); p.serr <- p.server.Run(ctx) }()
	go func() { defer p.wg.Done(); p.cerr <- p.client.Run(ctx) }()
	t.Cleanup(p.stop)
}

func (p *pair) stop() {
	if p.cancel != nil {
		p.cancel()
		p.wg.Wait()
		p.cancel = nil
	}
}

func (p *pair) waitUp(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if p.client.Up() && p.server.Up() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the pair did not come up (client up=%v, server up=%v)", p.client.Up(), p.server.Up())
}

func tcpEcho(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return l
}

func udpEcho(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, a, err := c.ReadFromUDP(buf)
			if err != nil {
				return
			}
			c.WriteToUDP(buf[:n], a)
		}
	}()
	return c
}
