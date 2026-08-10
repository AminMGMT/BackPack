package transport

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/xtaci/smux"
)

func newTestClientState(t *testing.T) (*clientState, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &clientState{}
	s.Reset(ctx, cancel, nil)
	return s, cancel
}

func TestClientStateStopClosesResourcesAndWaitsForWorkers(t *testing.T) {
	s, cancel := newTestClientState(t)
	defer cancel()
	server, peer := net.Pipe()
	defer peer.Close()

	if _, ok := s.Track(server); !ok {
		t.Fatal("live generation rejected a connection")
	}
	workerDone := make(chan struct{})
	if !s.Go(func() {
		defer close(workerDone)
		_, _ = server.Read(make([]byte, 1))
	}) {
		t.Fatal("live generation rejected a worker")
	}

	s.StopAndWait()
	select {
	case <-workerDone:
	default:
		t.Fatal("StopAndWait returned before the blocked worker exited")
	}
	if s.Go(func() {}) {
		t.Fatal("stopped generation accepted a new worker")
	}
}

func TestClientStateStopUnblocksSMUXAccept(t *testing.T) {
	s, cancel := newTestClientState(t)
	defer cancel()
	serverConn, peerConn := net.Pipe()
	defer peerConn.Close()

	session, err := smux.Server(serverConn, smux.DefaultConfig())
	if err != nil {
		t.Fatalf("create SMUX session: %v", err)
	}
	if _, ok := s.Track(serverConn); !ok {
		t.Fatal("live generation rejected the SMUX connection")
	}
	accepted := make(chan error, 1)
	s.Go(func() {
		_, err := session.AcceptStream()
		accepted <- err
	})

	s.StopAndWait()
	select {
	case err := <-accepted:
		if err == nil {
			t.Fatal("AcceptStream unexpectedly succeeded during shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("closing the generation did not unblock AcceptStream")
	}
}

func TestClientStateTrackAfterStopClosesImmediately(t *testing.T) {
	s, cancel := newTestClientState(t)
	defer cancel()
	s.StopAndWait()

	server, peer := net.Pipe()
	defer peer.Close()
	if _, ok := s.Track(server); ok {
		t.Fatal("stopped generation accepted a late connection")
	}
	peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("late connection was not closed")
	}
}
