package network

import (
	"time"

	"github.com/xtaci/smux"
)

// Agreeing on a mux version, because smux will not agree on one itself.
//
// smux has no version negotiation whatsoever. Each end is configured with a
// number, every frame carries it, and the first frame whose number does not
// match what the reader expects tears the session down with "invalid protocol".
// There is no fallback and no error the operator ever sees on the tunnel: the
// control channel is not muxed, so it comes up perfectly, and then no data
// passes. A tunnel that reports itself connected and carries nothing is the
// hardest failure in this whole system to diagnose.
//
// That is why the default could not simply be raised to 2, which is otherwise
// worth doing — version 1 has no per-stream flow control at all, so
// MaxStreamBuffer is silently ignored and one heavy stream can take the whole
// session's credit while the others wait. Raising it would have broken every
// mux tunnel at the moment one side was upgraded and the other was not.
//
// So the version is settled on the control channel instead, before any mux
// session exists. The server decides and says so in its handshake answer; the
// client uses what it is told, whatever its own file says. One end deciding is
// what makes disagreement impossible — two ends each applying their own
// configuration is exactly how the sessions end up mismatched.
//
// A client too old to be told anything speaks the older handshake, which the
// server answers without a version, and both stay on 1 as they always did.

// MuxVersionAuto is the configured value that means "negotiate it". It is the
// zero value, so a config that never mentions mux_version negotiates.
const MuxVersionAuto = 0

// ResolveMuxVersion turns a configured value into the version the server will
// impose. Auto becomes 2: the question is only ever asked on a handshake the
// client had to be new to speak, and a new client uses what it is told.
func ResolveMuxVersion(configured int) int {
	if configured == 1 || configured == 2 {
		return configured
	}
	return 2
}

// MuxSettings is the tuning both ends apply once they agree on a version.
type MuxSettings struct {
	MaxFrameSize     int
	MaxReceiveBuffer int
	MaxStreamBuffer  int
}

// SmuxConfig builds a session configuration for a settled version.
func SmuxConfig(version int, s MuxSettings) *smux.Config {
	return &smux.Config{
		Version:           version,
		KeepAliveInterval: 20 * time.Second,
		KeepAliveTimeout:  40 * time.Second,
		MaxFrameSize:      s.MaxFrameSize,
		MaxReceiveBuffer:  s.MaxReceiveBuffer,
		MaxStreamBuffer:   s.MaxStreamBuffer,
	}
}
