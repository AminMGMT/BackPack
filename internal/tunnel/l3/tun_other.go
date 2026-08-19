//go:build !linux

package l3

import (
	"fmt"
	"runtime"

	"github.com/sirupsen/logrus"
)

// Everything above the TUN device in this package is portable, and its tests
// run everywhere. Only the device itself is Linux-only, so only the device is
// stubbed: the encapsulation, the session and the replay window are all
// exercised by the ordinary test run on any platform.

type tunDevice struct{}

func (t *tunDevice) Name() string                { return "" }
func (t *tunDevice) MTU() int                    { return 0 }
func (t *tunDevice) Read(p []byte) (int, error)  { return 0, errNoTUN() }
func (t *tunDevice) Write(p []byte) (int, error) { return 0, errNoTUN() }
func (t *tunDevice) Close() error                { return nil }
func (t *tunDevice) SetMTU(int) error            { return errNoTUN() }

func openTUNTuned(name, localIP, peerIP string, mtu, mssClamp, txQueueLen int, qdisc string, log *logrus.Logger) (*tunDevice, error) {
	return nil, errNoTUN()
}

func errNoTUN() error {
	return fmt.Errorf("l3: a layer-3 tunnel needs a TUN device, which %s does not provide", runtime.GOOS)
}
