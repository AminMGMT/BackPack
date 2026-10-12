package enginectl

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestContextEndsAStatusRequestToAStalledEngine(t *testing.T) {
	previous := Dir
	Dir = t.TempDir()
	defer func() { Dir = previous }()
	listener, err := net.Listen("unix", SocketPath("stalled"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			<-release
		}
	}()
	defer func() { close(release); listener.Close(); <-done }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := AskContext(ctx, "stalled", OpStatus, nil); err == nil {
		t.Fatal("a stalled engine answered successfully")
	}
	if time.Since(started) > time.Second {
		t.Fatal("readiness query ignored its rollback deadline")
	}
}
