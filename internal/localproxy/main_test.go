package localproxy

import (
	"net"
	"os"
	"testing"

	"github.com/backpack/backpack/internal/socks"
)

// The tests have nothing to connect to but loopback, which the proxy refuses
// in earnest (see socks.Target); they widen it, and the test of the policy
// itself puts it back.
func TestMain(m *testing.M) {
	socks.Target = func(net.IP) bool { return true }
	os.Exit(m.Run())
}
