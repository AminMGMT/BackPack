package l3

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A direct tunnel whose flow the path stopped passing stayed down: the
// dialling side kept handshaking from the same socket, which is the same
// five-tuple, for as long as the generation lived — and it lived for ever.
// Changing the port in the config was the only way back, because that was a
// new flow. The generation now ends, and the restart loop reopens the carrier
// from a new source.
//
// Run over the tests' loopback carrier, which opens a fresh socket per
// generation exactly as pck, sni and xdi open fresh ports or a fresh echo
// identifier — and, unlike them, needs no raw socket. (It was the plain udp
// carrier until v2.0.0 removed it.)
func TestAnUnansweredFlowIsReopened(t *testing.T) {
	prev := flowStuckAfter
	flowStuckAfter = 0
	defer func() { flowStuckAfter = prev }()

	tun, err := New(Config{
		Mode: ModeDial, Addr: "127.0.0.1:9", Carrier: carrierLoopback, Token: "a-token-for-the-regression",
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dev := newFakeDevice(1400)
	tun.openDevice = func(deviceSpec) (packetDevice, error) { return dev, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- tun.Run(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, errFlowStuck) {
			t.Errorf("Run ended with %v, want the flow reported stuck", err)
		}
	case <-time.After(20 * time.Second):
		t.Error("a flow that answers nothing was kept for ever — only a new port would bring it back")
	}
}

func TestSpoofIsNotReopened(t *testing.T) {
	if stuckFlowCarrier(CarrierSpoof) {
		t.Error("a forged source is not a port this end can move")
	}
	for _, c := range []string{CarrierPck, CarrierSNI, CarrierXdi} {
		if !stuckFlowCarrier(c) {
			t.Errorf("%s is opened afresh from a new source and should be reopened", c)
		}
	}
}
