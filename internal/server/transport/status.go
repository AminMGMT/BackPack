package transport

import "strings"

// Running reports whether this transport's control channel is up.
//
// Every transport already publishes a one-line status for the panel, set to
// "Connected (…)" exactly when the control channel is established and cleared
// when it is not. That is the same fact a transport-fallback chain needs — see
// internal/tunnel/chain — so it is exposed here rather than invented again.
//
// One method per transport, because the status field is held by value on each
// struct and embedding it to share a method would change the memory layout of
// every one of them for no gain.

func (s *TcpTransport) Running() bool    { return connected(s.status.get()) }
func (s *TcpMuxTransport) Running() bool { return connected(s.status.get()) }
func (s *KcpTransport) Running() bool    { return connected(s.status.get()) }
func (s *QuicTransport) Running() bool   { return connected(s.status.get()) }
func (s *WsTransport) Running() bool     { return connected(s.status.get()) }
func (s *WsMuxTransport) Running() bool  { return connected(s.status.get()) }
func (s *UdpTransport) Running() bool    { return connected(s.status.get()) }

func connected(status string) bool { return strings.HasPrefix(status, "Connected") }
