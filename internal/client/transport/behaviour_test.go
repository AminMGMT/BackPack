package transport

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/utils/network"
	"github.com/gorilla/websocket"
)

// Behaviour on the client transports that can be exercised without a tunnel.
//
// This package is mostly network loops, and a loop that dials a peer cannot be
// unit tested without becoming an e2e test — which the suite already has, and
// which is where those paths are proved. What can be tested here is everything
// the loops *decide with*: the labels a panel reads, the settings a carrier is
// built from, the generation state that a restart swaps, and the small
// functions whose being wrong is silent.

func TestTransportLabelNamesTheCarrierNotTheProtocol(t *testing.T) {
	// The three carriers all run KCP's ARQ and are three different things to
	// an operator: a panel that called them all "KCP" would be describing the
	// implementation rather than what was configured.
	cases := []struct {
		cfg  KcpConfig
		want string
	}{
		{KcpConfig{}, "KCP"},
		{KcpConfig{UseICMP: true}, "XDI"},
		{KcpConfig{UsePck: true}, "PCK"},
	}
	for _, c := range cases {
		tr := &KcpTransport{config: &c.cfg}
		if got := tr.transportLabel(); got != c.want {
			t.Fatalf("label = %q, want %q for %+v", got, c.want, c.cfg)
		}
	}
}

// Every tuning value has to reach the carrier. A field added to the config and
// not carried across here is a setting that appears in the wizard, is written
// to the file, and does nothing.
func TestKcpSettingsCarryEveryTuningValue(t *testing.T) {
	cfg := &KcpConfig{
		MTU: 1350, Interval: 10, Resend: 2, NoDelay: 1, NoCongestion: 1,
		SndWnd: 512, RcvWnd: 1024, AckNoDelay: true,
		DataShards: 10, ParityShards: 3,
		SO_RCVBUF: 1 << 20, SO_SNDBUF: 2 << 20,
	}
	s := cfg.settings()
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"MTU", s.MTU, 1350}, {"Interval", s.Interval, 10}, {"Resend", s.Resend, 2},
		{"NoDelay", s.NoDelay, 1}, {"NoCongestion", s.NoCongestion, 1},
		{"SndWnd", s.SndWnd, 512}, {"RcvWnd", s.RcvWnd, 1024},
		{"AckNoDelay", s.AckNoDelay, true},
		{"DataShards", s.DataShards, 10}, {"ParityShards", s.ParityShards, 3},
		{"SO_RCVBUF", s.SO_RCVBUF, 1 << 20}, {"SO_SNDBUF", s.SO_SNDBUF, 2 << 20},
	} {
		if c.got != c.want {
			t.Fatalf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if s.Pck != nil {
		t.Fatal("a plain KCP tunnel was given a packet carrier")
	}
}

// A flag cycle that cannot be parsed must fall back rather than take the
// tunnel down: it is validated at load time, so reaching here at all means
// something unexpected, and a tunnel that refuses to start is worse than one
// on default flags.
func TestAnUnparseablePckFlagListFallsBack(t *testing.T) {
	cfg := &KcpConfig{UsePck: true, PckFlags: []string{"not-a-flag"}}
	s := cfg.settings()
	if s.Pck == nil {
		t.Fatal("a pck tunnel was not given a packet carrier")
	}
	want, _ := network.ParseTCPFlagList(nil)
	if len(s.Pck.Flags) != len(want) {
		t.Fatalf("flags = %v, want the default %v", s.Pck.Flags, want)
	}
}

func TestPckSettingsCarryTheInterfaceAndGateway(t *testing.T) {
	cfg := &KcpConfig{UsePck: true, PckInterface: "eth0", PckGatewayMAC: "aa:bb:cc:dd:ee:ff"}
	s := cfg.settings()
	if s.Pck == nil || s.Pck.Interface != "eth0" || s.Pck.GatewayMAC != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("pck carrier = %+v", s.Pck)
	}
}

// The udp transport cannot probe a backend with a TCP dial, so it does not
// balance across a list — but it must still accept one rather than trying to
// dial the whole pipe-separated string as an address.
func TestFirstBackendTakesOneAddressFromAList(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:80":                  "127.0.0.1:80",
		"127.0.0.1:80|127.0.0.2:80":     "127.0.0.1:80",
		" 127.0.0.1:80 | 127.0.0.2:80 ": "127.0.0.1:80",
		"":                              "",
	}
	for in, want := range cases {
		if got := firstBackend(in); got != want {
			t.Fatalf("firstBackend(%q) = %q, want %q", in, got, want)
		}
	}
}

// A write to a control channel that is not there must be an error, not a
// panic: the channel is nil for the whole window between a drop and the next
// handshake, and the heartbeat keeps firing through it.
func TestWritingToAnAbsentControlChannelIsAnError(t *testing.T) {
	if err := writeControl(nil, []byte{1}); err == nil {
		t.Fatal("writing to a nil control channel succeeded")
	}
}

func TestWritingToAControlChannelCarriesTheBytes(t *testing.T) {
	srv, cli := wsPair(t)
	if err := writeControl(cli, []byte{7, 8, 9}); err != nil {
		t.Fatalf("writeControl: %v", err)
	}
	kind, data, err := srv.ReadMessage()
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if kind != websocket.BinaryMessage {
		t.Fatalf("message type = %d, want binary", kind)
	}
	if string(data) != string([]byte{7, 8, 9}) {
		t.Fatalf("payload = %v", data)
	}
}

// The deadline writeControl sets must be cleared afterwards, or the next write
// on that connection inherits a deadline that has already passed and fails for
// a reason that has nothing to do with it.
func TestWriteControlClearsItsDeadline(t *testing.T) {
	srv, cli := wsPair(t)
	if err := writeControl(cli, []byte{1}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// Well past the write timeout: if the deadline were left in place this
	// second write would fail.
	time.Sleep(20 * time.Millisecond)
	if err := writeControl(cli, []byte{2}); err != nil {
		t.Fatalf("second write failed, so the deadline was left behind: %v", err)
	}
	srv.ReadMessage()
	if _, data, err := srv.ReadMessage(); err != nil || data[0] != 2 {
		t.Fatalf("second message = %v, %v", data, err)
	}
}

// Running() is what a fallback chain asks. It has to mean "the control channel
// is up" and nothing else — a transport that reports true while disconnected
// would pin a chain to a carrier that is not working.
func TestRunningTracksTheControlChannel(t *testing.T) {
	tr := &TcpTransport{}
	if tr.Running() {
		t.Fatal("a transport with no status reported itself running")
	}
	tr.status.set("Disconnected (TCP)")
	if tr.Running() {
		t.Fatal("a disconnected transport reported itself running")
	}
	tr.status.set("Connected (TCP)")
	if !tr.Running() {
		t.Fatal("a connected transport reported itself down")
	}
}

// Every transport has to answer, or a chain configured with it would sit on a
// carrier it can never confirm.
func TestEveryClientTransportAnswersRunning(t *testing.T) {
	each := []struct {
		name string
		set  func(string)
		run  func() bool
	}{}
	tcp := &TcpTransport{}
	each = append(each, struct {
		name string
		set  func(string)
		run  func() bool
	}{"tcp", tcp.status.set, tcp.Running})
	mux := &TcpMuxTransport{}
	each = append(each, struct {
		name string
		set  func(string)
		run  func() bool
	}{"tcpmux", mux.status.set, mux.Running})
	kcp := &KcpTransport{}
	each = append(each, struct {
		name string
		set  func(string)
		run  func() bool
	}{"kcp", kcp.status.set, kcp.Running})
	q := &QuicTransport{}
	each = append(each, struct {
		name string
		set  func(string)
		run  func() bool
	}{"quic", q.status.set, q.Running})
	ws := &WsTransport{}
	each = append(each, struct {
		name string
		set  func(string)
		run  func() bool
	}{"ws", ws.status.set, ws.Running})
	wsm := &WsMuxTransport{}
	each = append(each, struct {
		name string
		set  func(string)
		run  func() bool
	}{"wsmux", wsm.status.set, wsm.Running})
	udp := &UdpTransport{}
	each = append(each, struct {
		name string
		set  func(string)
		run  func() bool
	}{"udp", udp.status.set, udp.Running})

	if len(each) != 7 {
		t.Fatalf("checked %d transports, the package has 7", len(each))
	}
	for _, e := range each {
		if e.run() {
			t.Fatalf("%s reported running before anything happened", e.name)
		}
		e.set("Connected (x)")
		if !e.run() {
			t.Fatalf("%s did not report running when connected", e.name)
		}
		e.set("Disconnected (x)")
		if e.run() {
			t.Fatalf("%s kept reporting running after it dropped", e.name)
		}
	}
}

// clientState exists because Restart swaps a whole generation while the old
// one's goroutines are still reading it. The guarantee is that nobody can see
// half of one generation and half of another.
func TestResetPublishesAWholeGeneration(t *testing.T) {
	var s clientState
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.SetConn(&net.TCPConn{})
	s.Reset(ctx, cancel, nil)

	if s.Ctx() != ctx {
		t.Fatal("Reset did not publish the new context")
	}
	// The connections belong to the generation being replaced and must not
	// survive into the next one: a heartbeat writing to the old socket would
	// otherwise look, to the far end, like the new generation misbehaving.
	if s.Conn() != nil || s.WSConn() != nil {
		t.Fatal("Reset carried the old generation's connections forward")
	}
}

func TestClientStateIsSafeUnderConcurrentRestarts(t *testing.T) {
	var s clientState
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers, as the goroutines of a winding-down generation would be.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = s.Ctx()
					_ = s.Conn()
					_ = s.WSConn()
					_ = s.Usage()
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		s.Reset(ctx, cancel, nil)
		s.SetConn(&net.TCPConn{})
		cancel()
	}
	close(stop)
	wg.Wait()
}

// drain empties a signal channel rather than replacing it. Replacing it races
// with the goroutines selecting on the old one, which is why this exists.
func TestDrainEmptiesWithoutReplacing(t *testing.T) {
	ch := make(chan struct{}, 4)
	for i := 0; i < 3; i++ {
		ch <- struct{}{}
	}
	drain(ch)
	if len(ch) != 0 {
		t.Fatalf("drain left %d signals", len(ch))
	}
	// Still usable: a drain that closed or replaced it would break every
	// goroutine already selecting on it.
	ch <- struct{}{}
	if len(ch) != 1 {
		t.Fatal("the channel was not usable after draining")
	}
	// And draining an empty channel must return rather than block.
	drain(ch)
	drain(ch)
}

// The status is read by the panel from one goroutine while two generations
// write it from others.
func TestTunnelStatusIsSafeUnderConcurrentWrites(t *testing.T) {
	var s tunnelStatus
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				s.set("Connected (x)")
				_ = s.get()
			}
		}(i)
	}
	wg.Wait()
	if !strings.HasPrefix(s.get(), "Connected") {
		t.Fatalf("status = %q", s.get())
	}
}

// wsPair gives a connected pair of websocket connections over loopback.
func wsPair(t *testing.T) (server, client *websocket.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	up := websocket.Upgrader{}
	done := make(chan *websocket.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- nil
			return
		}
		// A minimal handshake: the gorilla upgrader wants an http.Server, so
		// this serves exactly one request on the raw connection.
		srv := &oneShot{conn: c, up: up, out: done}
		srv.serve()
	}()

	client, _, err = websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/", nil)
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	server = <-done
	if server == nil {
		t.Fatal("the server end never came up")
	}
	t.Cleanup(func() { server.Close() })
	return server, client
}
