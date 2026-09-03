package l3

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/sirupsen/logrus"
)

// The listening side must report a peer as soon as a handshake reaches it,
// before any traffic crosses.
//
// It did not. The dialling side published its peer when the handshake
// completed; the listening side waited for an authenticated *data* packet,
// because that is what promotes a pending session. On a tunnel that was up and
// simply idle, the listener therefore published nothing — and the two panels
// disagreed about a tunnel that was fine: green on one machine, red on the
// other, for as long as nobody sent anything through it.
//
// Waiting is right for deciding which keys to seal with, and wrong for deciding
// what the screens say. A completed handshake proves the peer holds the token.
//
// Only the listener runs here. The reported peer is a process-wide value, so a
// test with both ends in it cannot tell which of them published — an earlier
// version of this test passed with the fix reverted for exactly that reason.
func TestTheListenerReportsAPeerOnTheHandshakeAlone(t *testing.T) {
	metrics.ClearPeer()
	t.Cleanup(metrics.ClearPeer)

	const token = "an-idle-tunnel-token"
	dev := newFakeDevice(1400)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	listener, err := New(Config{
		Mode: ModeListen, Addr: "127.0.0.1:0", Token: token, Encap: "gre",
		LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1400,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New(listener): %v", err)
	}
	listener.openDevice = func(deviceSpec) (packetDevice, error) {
		return dev, nil
	}
	start(t, ctx, cancel, listener, dev)

	bound := awaitBind(t, listener)
	if metrics.SnapshotPeer() != "" {
		t.Fatal("a listener with no peer at all reported one")
	}

	// One handshake, sent by hand, and nothing else. No data packet follows,
	// which is the whole point.
	conn, err := net.Dial("udp", bound.String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	attempt, err := beginHandshake(token, 0, "gre")
	if err != nil {
		t.Fatalf("beginHandshake: %v", err)
	}
	if _, err := conn.Write(attempt.datagram()); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if metrics.SnapshotPeer() != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the listening side reported no peer after a completed handshake — " +
		"its panel shows offline while the dialling side shows online, until traffic happens to cross")
}

// A rekey must not be logged as a new connection.
//
// It was, and it cost somebody an afternoon: the dialling side rekeys every two
// minutes by design, and every one of them printed "session ... established".
// Read down a journal, that is indistinguishable from a tunnel dropping and
// redialling every two minutes, so it was reported as exactly that.
func TestARekeyIsNotLoggedAsANewConnection(t *testing.T) {
	tunnel, err := New(Config{
		Mode: ModeDial, Addr: "1.2.3.4:9000", Token: "t",
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2",
	}, quietLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var lines []string
	tunnel.log = captureLogger(&lines)
	tunnel.peer = &net.UDPAddr{IP: net.ParseIP("203.0.113.9"), Port: 9000}

	tunnel.installDialed(&session{id: 1, created: time.Now()})
	tunnel.installDialed(&session{id: 2, created: time.Now()})

	if len(lines) != 2 {
		t.Fatalf("expected two log lines, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "established") {
		t.Errorf("the first session was not reported as established: %q", lines[0])
	}
	if strings.Contains(lines[1], "established") {
		t.Errorf("a rekey was reported as a new connection: %q", lines[1])
	}
	if !strings.Contains(lines[1], "rekey") {
		t.Errorf("a rekey does not say so: %q", lines[1])
	}
	// And it must say the tunnel stayed up, because that is the question the
	// person reading the log actually has.
	if !strings.Contains(lines[1], "did not drop") {
		t.Errorf("the rekey line does not reassure that the tunnel held: %q", lines[1])
	}
}

// captureLogger records Info lines so a test can read what an operator would.
func captureLogger(into *[]string) *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	l.AddHook(&lineHook{into: into})
	return l
}

type lineHook struct{ into *[]string }

func (h *lineHook) Levels() []logrus.Level { return logrus.AllLevels }
func (h *lineHook) Fire(e *logrus.Entry) error {
	*h.into = append(*h.into, e.Message)
	return nil
}
