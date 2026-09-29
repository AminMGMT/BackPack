package socks

import (
	"net"
	"os"
	"testing"
)

// The tests have nothing to connect to but loopback, which the proxy refuses
// in earnest (see socks.Target); they widen it, and the test of the policy
// itself puts it back.
func TestMain(m *testing.M) {
	Target = func(net.IP) bool { return true }
	os.Exit(m.Run())
}
