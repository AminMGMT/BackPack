package core

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// A dial error names the address dialed, not "<nil>->" before it.
func TestADialErrorDoesNotStartWithANilSource(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	_, err = TcpDialer(context.Background(), addr, "", time.Second, time.Second, true, 1, 0, 0, 0)
	if err == nil {
		t.Fatal("a closed port accepted the connection")
	}
	if strings.Contains(err.Error(), "<nil>") {
		t.Fatalf("dial error: %v", err)
	}
}
