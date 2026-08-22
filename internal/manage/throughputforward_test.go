package manage

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// A port forwarder is measured through a mapping it already carries, so which
// mappings can carry one — and which cannot, and why — is the whole of the
// decision this screen makes on the operator's behalf.
func TestWhichMappingsCanCarryAMeasurement(t *testing.T) {
	for _, tc := range []struct {
		name       string
		spec       string
		usable     bool
		listen     int
		target     int
		reasonHint string
	}{
		{
			name:   "a bare port forwards to the same port on the far end",
			spec:   "443",
			usable: true, listen: 443, target: 443,
		},
		{
			name:   "an explicit loopback backend is reachable",
			spec:   "8443=127.0.0.1:9443",
			usable: true, listen: 8443, target: 9443,
		},
		{
			name:       "a backend on another machine cannot be sunk here",
			spec:       "443=10.0.0.5:443",
			usable:     false,
			reasonHint: "another machine",
		},
		{
			name:       "a load-balanced mapping has no single port to name",
			spec:       "443=127.0.0.1:8001|127.0.0.1:8002",
			usable:     false,
			reasonHint: "load-balanced",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := forwardMappings(Tunnel{Ports: []string{tc.spec}})
			if err != nil {
				t.Fatalf("expanding %q: %v", tc.spec, err)
			}
			if len(got) != 1 {
				t.Fatalf("%q expanded to %d mappings, want 1", tc.spec, len(got))
			}
			m := got[0]
			if m.Usable() != tc.usable {
				t.Fatalf("%q usable = %v (reason %q), want %v", tc.spec, m.Usable(), m.Reason, tc.usable)
			}
			if !tc.usable {
				if !strings.Contains(m.Reason, tc.reasonHint) {
					t.Errorf("reason for %q was %q, which does not explain %q", tc.spec, m.Reason, tc.reasonHint)
				}
				return
			}
			if m.ListenPort != tc.listen || m.TargetPort != tc.target {
				t.Errorf("%q → listen %d target %d, want %d and %d",
					tc.spec, m.ListenPort, m.TargetPort, tc.listen, tc.target)
			}
		})
	}
}

// A range expands to one mapping per port, and every one of them is measurable.
func TestARangeGivesAMeasurableMappingPerPort(t *testing.T) {
	got, err := forwardMappings(Tunnel{Ports: []string{"2053-2055"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("2053-2055 expanded to %d mappings, want 3", len(got))
	}
	for _, m := range got {
		if !m.Usable() {
			t.Errorf("%s should be measurable, but: %s", m.Spec, m.Reason)
		}
		if m.ListenPort != m.TargetPort {
			t.Errorf("%s maps %d to %d; a bare range forwards each port to itself",
				m.Spec, m.ListenPort, m.TargetPort)
		}
	}
}

// The list must keep the side that holds no port list of its own. That side is
// where the receiver runs, and dropping it would empty the menu on exactly the
// machine the sink belongs on.
func TestTheBackendSideStaysInTheList(t *testing.T) {
	kharej := Tunnel{Name: "k", Role: "client", Transport: "kcp"} // no Ports
	iranNoPorts := Tunnel{Name: "i", Role: "server", Transport: "kcp"}
	iranWithPorts := Tunnel{Name: "i2", Role: "server", Transport: "kcp", Ports: []string{"443"}}

	if !isForwardKind(kharej) || HoldsPorts(kharej) {
		t.Fatal("a reverse client is the forwarding kind and does not hold the ports")
	}
	// The rule the list applies, stated directly: only a port-holding side with
	// nothing configured is dropped.
	for _, tc := range []struct {
		t    Tunnel
		keep bool
	}{
		{kharej, true},
		{iranWithPorts, true},
		{iranNoPorts, false},
	} {
		drop := isForwardKind(tc.t) && HoldsPorts(tc.t) && len(tc.t.Ports) == 0
		if drop == tc.keep {
			t.Errorf("%s: dropped=%v, want kept=%v", tc.t.Name, drop, tc.keep)
		}
	}
}

// An l3 tunnel is still measured the old way, across its private subnet.
func TestLayerThreeIsNotTreatedAsAForwarder(t *testing.T) {
	if isForwardKind(Tunnel{Transport: "l3/pck"}) {
		t.Error("an l3 tunnel has a subnet of its own and must not borrow a mapping")
	}
	for _, tr := range []string{"kcp", "tcp", "wsmux", "direct/wss"} {
		if !isForwardKind(Tunnel{Transport: tr}) {
			t.Errorf("%s forwards ports and must be measured through one", tr)
		}
	}
}

// portIsBound is what stops the sink from being started on top of a live
// backend, so it has to be right in both directions.
func TestPortIsBoundSeesALiveListener(t *testing.T) {
	ln, err := net.Listen("tcp", forwardBackendHost+":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	fmt.Sscanf(p, "%d", &port)

	if !portIsBound(port) {
		t.Error("a port with a listener on it was reported free; the sink would fail to bind")
	}
	ln.Close()
	// Give the kernel a moment to release it.
	time.Sleep(100 * time.Millisecond)
	if portIsBound(port) {
		t.Error("a released port was reported busy; the operator would be sent to stop nothing")
	}
}

// The sink and the sender have to agree on a port other than the default, or
// nothing about the forwarding path works.
func TestTheMeasurementRunsOnAChosenPort(t *testing.T) {
	ln, err := net.Listen("tcp", forwardBackendHost+":0")
	if err != nil {
		t.Fatal(err)
	}
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	fmt.Sscanf(p, "%d", &port)
	ln.Close()
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := make(chan error, 1)
	go func() { sink <- ServeThroughputOn(ctx, forwardBackendHost, port) }()
	time.Sleep(200 * time.Millisecond)

	measure, cancelMeasure := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelMeasure()
	res, err := MeasureThroughputOn(measure, forwardBackendHost, port)
	if err != nil {
		t.Fatalf("measuring on the chosen port failed: %v", err)
	}
	if res.Bytes == 0 || res.Mbps() <= 0 {
		t.Errorf("the measurement moved nothing: %s", res)
	}

	cancel()
	if err := <-sink; err != nil {
		t.Errorf("the sink reported an error: %v", err)
	}
}
