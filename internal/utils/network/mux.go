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

// ResolveStaticMuxVersion is the choice for every mux transport that cannot
// negotiate — KCP and xDi (whose control channel is itself a smux session, so
// there is no muxless channel to agree over) and the websocket-mux transports
// (which were simply never given a negotiation handshake). Both ends resolve
// independently and must land on the same number from the same config, which
// they do because the resolution is deterministic.
//
// Auto becomes 1, not 2, and the difference is the whole point: these transports
// used version 1 on the previous release, so a 1.7.0 end meeting a 1.6.5 end has
// to choose 1 or the two disagree and smux tears every session down. An operator
// who wants 2 sets it explicitly on both ends. (smux rejects any version that is
// not 1 or 2 outright — "unsupported protocol version" — which is what a bare
// auto value of 0 produced before this existed, and is the regression this
// prevents.)
//
// This is deliberately not ResolveMuxVersion: that one answers auto with 2,
// because it is only ever consulted on the tcp/tcpmux negotiation handshake,
// where the server imposes the version and a new client obeys. Here there is no
// one to impose it, so backward compatibility decides.
func ResolveStaticMuxVersion(configured int) int {
	if configured == 1 || configured == 2 {
		return configured
	}
	return 1
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
