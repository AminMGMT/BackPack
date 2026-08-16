//go:build !linux

package network

import (
	"errors"
	"net"
	"time"
)

// The XDP receive fast path is Linux only. On every other platform the
// constructor fails, and the carrier falls back to its ordinary receive — the
// same graceful degradation as when the kernel is too old on Linux.

type spoofXDPReceiver struct{}

var errXDPUnsupported = errors.New("spoof: the XDP receive fast path is only available on Linux")

func newSpoofXDPReceiver(spoofXDPConfig) (*spoofXDPReceiver, error) { return nil, errXDPUnsupported }

func (*spoofXDPReceiver) read([]byte) (net.IP, int, error) { return nil, 0, errXDPUnsupported }

func (*spoofXDPReceiver) setReadDeadline(time.Time) {}

func (*spoofXDPReceiver) Close() error { return nil }
