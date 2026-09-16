package cmd

import (
	"testing"

	"github.com/backpack/backpack/config"
)

// Every transport whose data leaves by something other than the TCP dialer has
// to be on the refusal list, or its outbound settings are accepted and ignored.
//
// pck was not. It shares its case in client.go with kcp and xdi — all three end
// up in KcpConfig, which has no Outbound field — so a pck tunnel with a proxy,
// a local_addr, an interface or an so_mark passed the check, had "the tunnel
// server will be reached ..." written into its own log, and then dialled by
// whatever route the kernel chose. On a multi-homed host that is the tunnel
// leaving by the wrong uplink while the operator has been told it does not.
//
// The table is exhaustive on purpose: a new transport added without a decision
// here fails this test rather than silently joining the wrong half.
func TestEveryTransportOutsideTheTCPDialerRefusesOutboundSettings(t *testing.T) {
	for _, tc := range []struct {
		transport config.TransportType
		ignores   bool
		why       string
	}{
		{config.TCP, false, "a plain TCP dial, which is what these settings are"},
		{config.STEALTH, false, "TCP with a Noise record layer over it"},
		{config.TCPMUX, false, "TCP with smux over it"},
		{config.WS, false, "an HTTP upgrade over TCP"},
		{config.WSS, false, "TLS over TCP"},
		{config.WSMUX, false, "smux over a websocket over TCP"},
		{config.WSSMUX, false, "smux over a secure websocket over TCP"},

		{config.UDP, true, "datagrams, not a dialled stream"},
		{config.KCP, true, "KCP over UDP"},
		{config.QUIC, true, "QUIC over UDP"},
		{config.XDI, true, "KCP inside ICMP echo, through a raw socket"},
		{config.PCK, true, "KCP inside TCP segments built by hand, through a packet socket"},
		{config.SPOOF, true, "raw IP with a forged source"},
	} {
		if got := transportIgnoresOutbound(tc.transport); got != tc.ignores {
			t.Errorf("transportIgnoresOutbound(%q) = %v, want %v — %s",
				tc.transport, got, tc.ignores, tc.why)
		}
	}
}
